package main

import (
	"context"
	"encoding/json"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

// OIDC talks to Keycloak. The browser uses the public URL; the server uses
// the internal URL for tokens and keys, and checks the public issuer.
type OIDC struct {
	conf        oauth2.Config
	verifier    *oidc.IDTokenVerifier // access tokens (any audience)
	publicBase  string
	logoutURL   string
}

func newOIDC(ctx context.Context) *OIDC {
	pub := strings.TrimRight(os.Getenv("OIDC_PUBLIC_ISSUER"), "/")
	internal := strings.TrimRight(env("OIDC_INTERNAL_ISSUER", pub), "/")
	o := &OIDC{publicBase: pub}
	o.conf = oauth2.Config{
		ClientID:     env("OIDC_CLIENT_ID", "agentbox-console"),
		ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("OIDC_REDIRECT_URL"),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  pub + "/protocol/openid-connect/auth",
			TokenURL: internal + "/protocol/openid-connect/token",
		},
	}
	keys := oidc.NewRemoteKeySet(ctx, internal+"/protocol/openid-connect/certs")
	o.verifier = oidc.NewVerifier(pub, keys, &oidc.Config{SkipClientIDCheck: true})
	o.logoutURL = pub + "/protocol/openid-connect/logout"
	return o
}

type Claims struct {
	Username string   `json:"preferred_username"`
	Email    string   `json:"email"`
	Azp      string   `json:"azp"`
	Groups   []string `json:"groups"`
	Realm    struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (o *OIDC) verifyAccess(ctx context.Context, raw string) (*Claims, error) {
	tok, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	var c Claims
	if err := tok.Claims(&c); err != nil {
		return nil, err
	}
	if c.Username == "" {
		return nil, fmt.Errorf("token has no preferred_username")
	}
	return &c, nil
}

// ---------- sessions ----------

type session struct {
	User    string
	Email   string
	Roles   []string
	Expires time.Time
	IDToken string
}

func (s session) Role() string {
	for _, r := range []string{"admin", "approver", "viewer"} {
		for _, x := range s.Roles {
			if x == r {
				return r
			}
		}
	}
	return ""
}

func (s session) Can(role string) bool {
	rank := map[string]int{"viewer": 1, "approver": 2, "admin": 3}
	return rank[s.Role()] >= rank[role]
}

// sessions are kept in Postgres, so a console restart or a second replica
// does not sign administrators out.
type sessions struct{ pool *pgxpool.Pool }

func newSessions(pool *pgxpool.Pool) *sessions { return &sessions{pool: pool} }

func (st *sessions) get(id string) *session {
	var ss session
	var roles []byte
	err := st.pool.QueryRow(context.Background(), `SELECT username, email, roles, id_token, expires_at FROM console_sessions WHERE id=$1 AND expires_at > now()`, id).
		Scan(&ss.User, &ss.Email, &roles, &ss.IDToken, &ss.Expires)
	if err != nil {
		return nil
	}
	json.Unmarshal(roles, &ss.Roles)
	return &ss
}

func (st *sessions) put(id string, ss *session) {
	roles, _ := json.Marshal(ss.Roles)
	st.pool.Exec(context.Background(), `INSERT INTO console_sessions(id, username, email, roles, id_token, expires_at) VALUES($1,$2,$3,$4,$5,$6)`,
		id, ss.User, ss.Email, roles, ss.IDToken, ss.Expires)
	st.pool.Exec(context.Background(), `DELETE FROM console_sessions WHERE expires_at < now()`)
}

func (st *sessions) del(id string) {
	st.pool.Exec(context.Background(), `DELETE FROM console_sessions WHERE id=$1`, id)
}

func (st *sessions) putState(state string) {
	st.pool.Exec(context.Background(), `INSERT INTO oauth_states(state) VALUES($1)`, state)
	st.pool.Exec(context.Background(), `DELETE FROM oauth_states WHERE created_at < now() - interval '15 minutes'`)
}

func (st *sessions) takeState(state string) bool {
	tag, err := st.pool.Exec(context.Background(), `DELETE FROM oauth_states WHERE state=$1 AND created_at > now() - interval '15 minutes'`, state)
	return err == nil && tag.RowsAffected() == 1
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) current(r *http.Request) *session {
	c, err := r.Cookie("abx_session")
	if err != nil {
		return nil
	}
	return s.sess.get(c.Value)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	state := randHex(16)
	s.sess.putState(state)
	http.Redirect(w, r, s.oidc.conf.AuthCodeURL(state), http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	ok := s.sess.takeState(state)
	if !ok {
		http.Error(w, "unknown login state; start again at /auth/login", 400)
		return
	}
	tok, err := s.oidc.conf.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "login failed: "+err.Error(), 400)
		return
	}
	c, err := s.oidc.verifyAccess(r.Context(), tok.AccessToken)
	if err != nil {
		http.Error(w, "token invalid: "+err.Error(), 400)
		return
	}
	ss := &session{User: c.Username, Email: c.Email, Roles: c.Realm.Roles, Expires: time.Now().Add(8 * time.Hour)}
	if idt, ok := tok.Extra("id_token").(string); ok {
		ss.IDToken = idt
	}
	if ss.Role() == "" {
		http.Error(w, c.Username+" has no Agentbox console role (viewer, approver or admin)", 403)
		return
	}
	id := randHex(24)
	s.sess.put(id, ss)
	http.SetCookie(w, &http.Cookie{Name: "abx_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: ss.Expires})
	s.audit(r.Context(), ss.User, ss.Role(), "console.login", "", map[string]any{"email": ss.Email})
	log.Printf("console login: %s (%s)", ss.User, ss.Role())
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	ss := s.current(r)
	if c, err := r.Cookie("abx_session"); err == nil {
		s.sess.del(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "abx_session", Value: "", Path: "/", MaxAge: -1})
	u := s.oidc.logoutURL + "?client_id=" + s.oidc.conf.ClientID + "&post_logout_redirect_uri=" + strings.TrimSuffix(s.oidc.conf.RedirectURL, "/auth/callback") + "/"
	if ss != nil && ss.IDToken != "" {
		u += "&id_token_hint=" + ss.IDToken
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// need wraps a handler with a minimum role.
func (s *Server) need(role string, h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ss := s.current(r)
		if ss == nil {
			fail(w, 401, "sign in required")
			return
		}
		if !ss.Can(role) {
			s.audit(r.Context(), ss.User, ss.Role(), "denied", r.Method+" "+r.URL.Path, map[string]any{"needed": role})
			fail(w, 403, fmt.Sprintf("%s role required; you are %s", role, ss.Role()))
			return
		}
		h(w, r, ss)
	}
}
