// permsvc is the permission service and online policy signer. Appliances talk
// to it over mutual TLS: they long-poll for signed bundles and admin commands,
// report status, and file self-service permission requests, which OPA sorts
// into instant, flagged, approval or never. It is the only bundle writer.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"agentbox/pkg/bundle"
	"agentbox/pkg/db"
	"agentbox/pkg/identity"
	"agentbox/pkg/otlp"
	"agentbox/pkg/pki"
	"agentbox/pkg/policy"
)

type svc struct {
	pool *pgxpool.Pool
	key  ed25519.PrivateKey
	cls  *policy.Classifier
	ev   *otlp.Exporter
	hub  *hub
	bmu  sync.Mutex // serializes bundle rebuilds
}

func main() {
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	db.WaitSchema(ctx, pool)
	key, err := bundle.LoadPrivateKey(os.Getenv("POLICY_KEY_FILE"))
	if err != nil {
		log.Fatal(err)
	}
	cls, err := policy.NewClassifier(ctx)
	if err != nil {
		log.Fatal(err)
	}
	s := &svc{pool: pool, key: key, cls: cls, hub: newHub(),
		ev: otlp.New(os.Getenv("OTLP_ENDPOINT"), map[string]string{"service.name": "permsvc"}, nil)}

	go db.Listen(ctx, pool, func(ch, payload string) {
		switch ch {
		case db.ChanPolicy:
			if payload == "*" || payload == "" {
				s.rebuildAll(ctx)
			} else {
				s.rebuildUserOrDevice(ctx, payload)
			}
		case db.ChanBundle, db.ChanCommands:
			s.hub.wake(payload)
		}
	}, db.ChanPolicy, db.ChanBundle, db.ChanCommands)
	go func() {
		s.rebuildAll(ctx)
		for range time.Tick(5 * time.Minute) {
			s.expireGrants(ctx)
			s.rebuildAll(ctx)
		}
	}()

	pool2, err := pki.LoadPool(os.Getenv("CLIENT_CA_FILE"))
	if err != nil {
		log.Fatal(err)
	}
	tlsCfg, err := pki.ServerTLS(os.Getenv("TLS_CERT_FILE"), os.Getenv("TLS_KEY_FILE"), pool2, true)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sync", s.sync)
	mux.HandleFunc("POST /v1/status", s.status)
	mux.HandleFunc("POST /v1/requests", s.request)
	mux.HandleFunc("GET /v1/requests", s.myRequests)
	mux.HandleFunc("GET /v1/requests/{id}", s.getRequest)
	mux.HandleFunc("POST /v1/commands/{id}/ack", s.ack)
	go http.ListenAndServe(":8081", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	srv := &http.Server{Addr: envOr("LISTEN", ":8443"), Handler: mux, TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("permsvc listening on %s (mTLS)", srv.Addr)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// caller authenticates the appliance and checks it is enrolled and not revoked.
func (s *svc) caller(w http.ResponseWriter, r *http.Request) (identity.Appliance, bool) {
	a, err := identity.FromRequest(r)
	if err != nil {
		fail(w, 401, err.Error())
		return a, false
	}
	var owner, reason string
	var revoked bool
	err = s.pool.QueryRow(r.Context(), `SELECT username, revoked, revoked_reason FROM devices WHERE device_id=$1`, a.Device).Scan(&owner, &revoked, &reason)
	if err != nil {
		fail(w, 403, "device not enrolled")
		return a, false
	}
	if owner != a.User {
		fail(w, 403, "certificate user does not own this device")
		return a, false
	}
	if revoked {
		fail(w, 403, "device revoked: "+reason)
		return a, false
	}
	return a, true
}

// ---------- long-poll hub ----------

type hub struct {
	mu   sync.Mutex
	subs map[string][]chan struct{}
}

func newHub() *hub { return &hub{subs: map[string][]chan struct{}{}} }

func (h *hub) sub(dev string) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := make(chan struct{}, 1)
	h.subs[dev] = append(h.subs[dev], c)
	return c
}

func (h *hub) unsub(dev string, c chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.subs[dev]
	for i, x := range l {
		if x == c {
			h.subs[dev] = append(l[:i], l[i+1:]...)
			break
		}
	}
}

func (h *hub) wake(dev string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for d, l := range h.subs {
		if d == dev || dev == "*" {
			for _, c := range l {
				select {
				case c <- struct{}{}:
				default:
				}
			}
		}
	}
}

type command struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Args      json.RawMessage `json:"args"`
	CreatedBy string          `json:"created_by"`
	Reason    string          `json:"reason"`
}

// sync returns a newer bundle and/or pending commands, waiting up to `wait`
// seconds for one to appear.
func (s *svc) sync(w http.ResponseWriter, r *http.Request) {
	a, ok := s.caller(w, r)
	if !ok {
		return
	}
	have, _ := strconv.ParseInt(r.URL.Query().Get("have"), 10, 64)
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	if wait <= 0 || wait > 55 {
		wait = 25
	}
	c := s.hub.sub(a.Device)
	defer s.hub.unsub(a.Device, c)
	deadline := time.After(time.Duration(wait) * time.Second)
	for {
		out := map[string]any{}
		var raw []byte
		var ver int64
		err := s.pool.QueryRow(r.Context(), `SELECT version, signed FROM bundles WHERE device_id=$1`, a.Device).Scan(&ver, &raw)
		if err == nil && ver > have {
			out["bundle"] = json.RawMessage(raw)
		} else if errors.Is(err, pgx.ErrNoRows) {
			s.rebuild(r.Context(), a.Device)
		}
		rows, err := s.pool.Query(r.Context(), `SELECT id, kind, args, created_by, reason FROM commands WHERE device_id=$1 AND acked_at IS NULL ORDER BY id`, a.Device)
		if err == nil {
			var cmds []command
			for rows.Next() {
				var c command
				rows.Scan(&c.ID, &c.Kind, &c.Args, &c.CreatedBy, &c.Reason)
				cmds = append(cmds, c)
			}
			rows.Close()
			if len(cmds) > 0 {
				out["commands"] = cmds
			}
		}
		if len(out) > 0 {
			writeJSON(w, 200, out)
			return
		}
		select {
		case <-c:
		case <-deadline:
			writeJSON(w, 200, map[string]any{})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *svc) status(w http.ResponseWriter, r *http.Request) {
	a, ok := s.caller(w, r)
	if !ok {
		return
	}
	var st struct {
		BundleVersion    int64           `json:"bundle_version"`
		ApplianceVersion string          `json:"appliance_version"`
		Sandboxes        json.RawMessage `json:"sandboxes"`
	}
	raw := json.RawMessage{}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&raw); err != nil {
		fail(w, 400, err.Error())
		return
	}
	json.Unmarshal(raw, &st)
	var prev []byte
	var prevVer int64
	s.pool.QueryRow(r.Context(), `SELECT status, bundle_version FROM devices WHERE device_id=$1`, a.Device).Scan(&prev, &prevVer)
	_, err := s.pool.Exec(r.Context(), `UPDATE devices SET last_seen=now(), bundle_version=$2, appliance_version=$3, status=$4 WHERE device_id=$1`,
		a.Device, st.BundleVersion, st.ApplianceVersion, []byte(raw))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if string(prev) != string(raw) || prevVer != st.BundleVersion {
		db.Notify(r.Context(), s.pool, db.ChanConsole, "devices")
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *svc) ack(w http.ResponseWriter, r *http.Request) {
	a, ok := s.caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Result string `json:"result"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	_, err := s.pool.Exec(r.Context(), `UPDATE commands SET acked_at=now(), result=$3 WHERE id=$1 AND device_id=$2`, r.PathValue("id"), a.Device, body.Result)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	db.Notify(r.Context(), s.pool, db.ChanConsole, "sandboxes")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- permission requests ----------

type reqOut struct {
	ID             int64      `json:"id"`
	Permission     string     `json:"permission"`
	Reason         string     `json:"reason"`
	Class          string     `json:"class"`
	Status         string     `json:"status"`
	DecidedBy      string     `json:"decided_by,omitempty"`
	DecisionReason string     `json:"decision_reason,omitempty"`
	ReviewStatus   string     `json:"review_status,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	Message        string     `json:"message,omitempty"`
}

func (s *svc) loadUserPolicy(ctx context.Context, user string) (policy.User, policy.GroupPolicy, policy.Catalog, []string, error) {
	u := policy.User{Name: user}
	if err := s.pool.QueryRow(ctx, `SELECT grp, tier FROM users WHERE username=$1`, user).Scan(&u.Group, &u.Tier); err != nil {
		return u, policy.GroupPolicy{}, policy.Catalog{}, nil, fmt.Errorf("user %s: %w", user, err)
	}
	g := s.group(ctx, u.Group)
	var cat policy.Catalog
	db.GetSetting(ctx, s.pool, "catalog", &cat)
	var rules []string
	rows, err := s.pool.Query(ctx, `SELECT pattern FROM auto_rules`)
	if err == nil {
		for rows.Next() {
			var p string
			rows.Scan(&p)
			rules = append(rules, p)
		}
		rows.Close()
	}
	return u, g, cat, rules, nil
}

func (s *svc) group(ctx context.Context, name string) policy.GroupPolicy {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT policy FROM groups WHERE name=$1`, name).Scan(&raw); err != nil {
		s.pool.QueryRow(ctx, `SELECT policy FROM groups WHERE name='default'`).Scan(&raw)
	}
	g := policy.DefaultGroup()
	if raw != nil {
		json.Unmarshal(raw, &g)
	}
	return g
}

func (s *svc) request(w http.ResponseWriter, r *http.Request) {
	a, ok := s.caller(w, r)
	if !ok {
		return
	}
	var in struct {
		Permission string `json:"permission"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		fail(w, 400, `a reason is required: agentbox allow <permission> --reason "..."`)
		return
	}
	ctx := r.Context()
	u, g, cat, rules, err := s.loadUserPolicy(ctx, a.User)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	class, err := s.cls.Classify(ctx, in.Permission, u, g, cat, rules)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	status, review := "pending", "none"
	var expires *time.Time
	msg := ""
	switch class {
	case "instant":
		status, msg = "active", "applied automatically"
	case "flagged":
		t := time.Now().Add(14 * 24 * time.Hour)
		status, review, expires = "active", "pending", &t
		msg = "applied now and flagged for review; it lasts 14 days unless renewed"
	case "approval":
		msg = "waiting for an approver in the Agentbox console (role: approver)"
	case "never":
		status, msg = "denied", "this permission is never allowed by policy"
	}
	var out reqOut
	err = s.pool.QueryRow(ctx, `INSERT INTO requests(username, device_id, permission, reason, class, status, review_status, expires_at, decision_reason, decided_by, decided_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, CASE WHEN $6 IN ('active','denied') THEN now() END) RETURNING id, created_at`,
		a.User, a.Device, in.Permission, in.Reason, class, status, review, expires,
		map[bool]string{true: msg, false: ""}[class == "never" || class == "instant" || class == "flagged"],
		map[bool]string{true: "policy", false: ""}[class != "approval"]).Scan(&out.ID, &out.CreatedAt)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out.Permission, out.Reason, out.Class, out.Status, out.ExpiresAt, out.Message, out.ReviewStatus = in.Permission, in.Reason, class, status, expires, msg, review
	detail, _ := json.Marshal(map[string]any{"permission": in.Permission, "reason": in.Reason, "class": class, "device": a.Device})
	s.pool.Exec(ctx, `INSERT INTO audit(actor, role, action, target, detail) VALUES($1,'user','permission.request',$2,$3)`, a.User, in.Permission, detail)
	s.ev.Emit("permission.requested", map[string]any{"agentbox.user": a.User, "agentbox.device": a.Device, "permission": in.Permission, "class": class, "reason": in.Reason})
	if status == "active" {
		s.rebuildUser(ctx, a.User)
	}
	db.Notify(ctx, s.pool, db.ChanConsole, "requests")
	writeJSON(w, 200, out)
}

func scanReq(row pgx.Row) (reqOut, error) {
	var o reqOut
	err := row.Scan(&o.ID, &o.Permission, &o.Reason, &o.Class, &o.Status, &o.DecidedBy, &o.DecisionReason, &o.ReviewStatus, &o.ExpiresAt, &o.CreatedAt)
	return o, err
}

const reqCols = `id, permission, reason, class, status, decided_by, decision_reason, review_status, expires_at, created_at`

func (s *svc) myRequests(w http.ResponseWriter, r *http.Request) {
	a, ok := s.caller(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+reqCols+` FROM requests WHERE username=$1 ORDER BY id DESC LIMIT 100`, a.User)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []reqOut{}
	for rows.Next() {
		if o, err := scanReq(rows); err == nil {
			out = append(out, o)
		}
	}
	writeJSON(w, 200, out)
}

// getRequest long-polls until the request leaves "pending" (or wait expires).
func (s *svc) getRequest(w http.ResponseWriter, r *http.Request) {
	a, ok := s.caller(w, r)
	if !ok {
		return
	}
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		o, err := scanReq(s.pool.QueryRow(r.Context(), `SELECT `+reqCols+` FROM requests WHERE id=$1 AND username=$2`, r.PathValue("id"), a.User))
		if err != nil {
			fail(w, 404, "no such request")
			return
		}
		if o.Status != "pending" || time.Now().After(deadline) {
			writeJSON(w, 200, o)
			return
		}
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
			return
		}
	}
}

// ---------- bundles ----------

func (s *svc) expireGrants(ctx context.Context) {
	tag, err := s.pool.Exec(ctx, `UPDATE requests SET status='expired' WHERE status IN ('active','approved') AND expires_at IS NOT NULL AND expires_at < now()`)
	if err == nil && tag.RowsAffected() > 0 {
		db.Notify(ctx, s.pool, db.ChanConsole, "requests")
	}
}

func (s *svc) rebuildAll(ctx context.Context) {
	rows, err := s.pool.Query(ctx, `SELECT device_id FROM devices WHERE NOT revoked`)
	if err != nil {
		log.Printf("rebuildAll: %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		s.rebuild(ctx, id)
	}
}

// rebuildUserOrDevice accepts "user:<name>" or a device id.
func (s *svc) rebuildUserOrDevice(ctx context.Context, p string) {
	if u, ok := strings.CutPrefix(p, "user:"); ok {
		s.rebuildUser(ctx, u)
		return
	}
	s.rebuild(ctx, p)
}

func (s *svc) rebuildUser(ctx context.Context, user string) {
	rows, err := s.pool.Query(ctx, `SELECT device_id FROM devices WHERE username=$1 AND NOT revoked`, user)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		s.rebuild(ctx, id)
	}
}

// rebuild recomputes a device bundle; a new signed version is stored only
// when the content changed or the current one is past half its lifetime.
func (s *svc) rebuild(ctx context.Context, device string) {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	var d policy.Device
	var revoked bool
	err := s.pool.QueryRow(ctx, `SELECT device_id, username, assurance_level, revoked FROM devices WHERE device_id=$1`, device).Scan(&d.ID, &d.User, &d.AssuranceLevel, &revoked)
	if err != nil || revoked {
		return
	}
	u, g, cat, _, err := s.loadUserPolicy(ctx, d.User)
	if err != nil {
		log.Printf("rebuild %s: %v", device, err)
		return
	}
	var grants []policy.Grant
	rows, err := s.pool.Query(ctx, `SELECT id, permission, class, expires_at FROM requests
		WHERE username=$1 AND status IN ('active','approved') AND (expires_at IS NULL OR expires_at > now()) ORDER BY id`, d.User)
	if err != nil {
		return
	}
	for rows.Next() {
		var gr policy.Grant
		rows.Scan(&gr.ID, &gr.Permission, &gr.Class, &gr.ExpiresAt)
		grants = append(grants, gr)
	}
	rows.Close()
	channel := "broad"
	db.GetSetting(ctx, s.pool, "release_channel:"+d.ID, &channel)
	b := policy.Build(d, u, g, cat, grants, channel)
	hash := policy.ContentHash(b)
	var curVer int64
	var curHash string
	var curExp time.Time
	err = s.pool.QueryRow(ctx, `SELECT version, content_hash, expires_at FROM bundles WHERE device_id=$1`, device).Scan(&curVer, &curHash, &curExp)
	if err == nil && curHash == hash && time.Until(curExp) > policy.BundleTTL/2 {
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	b.Version, b.IssuedAt, b.ExpiresAt = curVer+1, now, now.Add(policy.BundleTTL)
	signed, err := bundle.Sign(b, s.key)
	if err != nil {
		log.Printf("sign: %v", err)
		return
	}
	raw, _ := json.Marshal(signed)
	_, err = s.pool.Exec(ctx, `INSERT INTO bundles(device_id, version, content_hash, signed, expires_at) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(device_id) DO UPDATE SET version=$2, content_hash=$3, signed=$4, expires_at=$5, created_at=now()`,
		device, b.Version, hash, raw, b.ExpiresAt)
	if err != nil {
		log.Printf("store bundle: %v", err)
		return
	}
	log.Printf("bundle %s v%d signed (%d grants)", device, b.Version, len(grants))
	s.ev.Emit("bundle.signed", map[string]any{"agentbox.device": device, "agentbox.user": d.User, "agentbox.bundle_version": b.Version})
	db.Notify(ctx, s.pool, db.ChanBundle, device)
	db.Notify(ctx, s.pool, db.ChanConsole, "devices")
}

var _ = tls.VersionTLS12
