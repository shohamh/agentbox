// gatekeeper is the front gateway's decision point. Envoy calls it over
// ext_authz with the appliance's verified SPIFFE ID; it checks enrollment,
// revocation, attestation, kill switches, model and budget, then attaches the
// LiteLLM virtual key for that user, device and tier. It is the only holder of
// the LiteLLM master key, so no model credential ever exists on a PC.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	"agentbox/pkg/bundle"
	"agentbox/pkg/db"
	"agentbox/pkg/extauthz"
	"agentbox/pkg/identity"
	"agentbox/pkg/keymint"
	"agentbox/pkg/otlp"
)

type device struct {
	ID, User, Attestation, Level, RevokedReason string
	Revoked                                     bool
}

type user struct {
	Name, Group string
	Tier        int
	Budget      float64
	Suspended   bool
}

type kill struct {
	Enabled bool
	Reason  string
	By      string
}

type state struct {
	devices map[string]device
	users   map[string]user
	kills   map[string]kill
	models  map[string][]string // device -> models from its current bundle
	tiers   map[string]int      // device -> tier from its current bundle (the user's tier plus granted ones)
	spend   map[string]float64
}

type server struct {
	authv3.UnimplementedAuthorizationServer
	pool    *pgxpool.Pool
	llmDB   *pgxpool.Pool
	mint    *keymint.Minter
	ev      *otlp.Exporter

	mu sync.RWMutex
	st state
}

func main() {
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	db.WaitSchema(ctx, pool)
	var llmDB *pgxpool.Pool
	if u := os.Getenv("LITELLM_DATABASE_URL"); u != "" {
		llmDB, err = db.Open(ctx, u)
		if err != nil {
			log.Fatal(err)
		}
	}
	master, err := os.ReadFile(os.Getenv("LITELLM_MASTER_KEY_FILE"))
	if err != nil {
		log.Fatalf("master key: %v", err)
	}
	s := &server{
		pool: pool, llmDB: llmDB,
		mint: keymint.New(keymint.Config{
			LiteLLM: os.Getenv("LITELLM_URL"), Master: strings.TrimSpace(string(master)),
			TTL: envDur("KEY_TTL", 15*time.Minute), RenewBefore: envDur("KEY_RENEW_BEFORE", 0),
			RPM: envInt("KEY_RPM", 0), TPM: envInt("KEY_TPM", 0), Parallel: envInt("KEY_PARALLEL", 0),
		}),
		ev: otlp.New(os.Getenv("OTLP_ENDPOINT"), map[string]string{"service.name": "gatekeeper"}, nil),
	}
	s.refresh(ctx)
	go func() {
		for range time.Tick(2 * time.Second) {
			s.refresh(ctx)
		}
	}()
	log.Printf("model keys: one per sandbox, valid %s, minted on demand", s.mint.TTL())
	go func() {
		for range time.Tick(time.Minute) {
			if n := s.mint.Prune(); n > 0 {
				log.Printf("deleted %d expired LiteLLM keys", n)
			}
		}
	}()
	go http.ListenAndServe(":8082", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	lis, err := net.Listen("tcp", envOr("LISTEN", ":9191"))
	if err != nil {
		log.Fatal(err)
	}
	g := grpc.NewServer()
	authv3.RegisterAuthorizationServer(g, s)
	log.Printf("gatekeeper listening on %s", lis.Addr())
	log.Fatal(g.Serve(lis))
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func (s *server) refresh(ctx context.Context) {
	st := state{devices: map[string]device{}, users: map[string]user{}, kills: map[string]kill{}, models: map[string][]string{}, tiers: map[string]int{}, spend: map[string]float64{}}
	rows, err := s.pool.Query(ctx, `SELECT device_id, username, attestation_status, assurance_level, revoked, revoked_reason FROM devices`)
	if err != nil {
		log.Printf("refresh devices: %v", err)
		return
	}
	for rows.Next() {
		var d device
		rows.Scan(&d.ID, &d.User, &d.Attestation, &d.Level, &d.Revoked, &d.RevokedReason)
		st.devices[d.ID] = d
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT username, grp, tier, budget_usd::float8, suspended FROM users`)
	if err != nil {
		return
	}
	for rows.Next() {
		var u user
		rows.Scan(&u.Name, &u.Group, &u.Tier, &u.Budget, &u.Suspended)
		st.users[u.Name] = u
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT scope, enabled, reason, set_by FROM kill_switches`)
	if err != nil {
		return
	}
	for rows.Next() {
		var scope string
		var k kill
		rows.Scan(&scope, &k.Enabled, &k.Reason, &k.By)
		st.kills[scope] = k
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT device_id, signed FROM bundles`)
	if err != nil {
		return
	}
	for rows.Next() {
		var id string
		var raw []byte
		rows.Scan(&id, &raw)
		var sb bundle.Signed
		if json.Unmarshal(raw, &sb) == nil {
			if b, err := bundle.Decode(&sb); err == nil {
				st.models[id] = b.Models
				st.tiers[id] = b.Tier
			}
		}
	}
	rows.Close()
	if s.llmDB != nil {
		rows, err = s.llmDB.Query(ctx, `SELECT user_id, COALESCE(spend,0) FROM "LiteLLM_UserTable"`)
		if err == nil {
			for rows.Next() {
				var u string
				var sp float64
				rows.Scan(&u, &sp)
				st.spend[u] = sp
			}
			rows.Close()
		}
	}
	s.mu.Lock()
	old := s.st
	s.st = st
	s.mu.Unlock()
	// A device that stops being trusted (revoked, or attestation no longer "pass")
	// loses its LiteLLM keys now, not when they expire.
	for id, d := range st.devices {
		if o, had := old.devices[id]; had && trusted(o) && !trusted(d) {
			go func(id string) {
				if n := s.mint.RevokeDevice(id); n > 0 {
					log.Printf("deleted %d LiteLLM keys of %s (no longer trusted)", n, id)
				}
			}(id)
		}
	}
}

var modelRe = regexp.MustCompile(`"model"\s*:\s*"([^"]+)"`)

func levelCap(level string) int {
	switch level {
	case "A", "B":
		return 3
	}
	return 2
}

func (s *server) deny(code int, typ, msg string, attrs map[string]any) *authv3.CheckResponse {
	attrs["reason"] = typ
	attrs["message"] = msg
	s.ev.EmitSev("WARN", "gateway.denied", attrs)
	return extauthz.Deny(code, typ, "Agentbox: "+msg)
}

func (s *server) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	a := req.GetAttributes()
	h := a.GetRequest().GetHttp()
	headers := h.GetHeaders()
	principal := a.GetSource().GetPrincipal()
	ev := map[string]any{"principal": principal, "path": h.GetPath(), "agentbox.sandbox": headers["x-agentbox-sandbox"], "request_id": headers["x-request-id"]}
	kind, id, err := identity.Parse(principal)
	if err != nil || kind != "appliance" {
		return s.deny(401, "no_identity", "a valid appliance certificate is required", ev), nil
	}
	ev["agentbox.device"], ev["agentbox.user"] = id.Device, id.User
	s.mu.RLock()
	st := s.st
	s.mu.RUnlock()
	d, ok := st.devices[id.Device]
	switch {
	case !ok:
		return s.deny(403, "not_enrolled", "device "+id.Device+" is not enrolled", ev), nil
	case d.User != id.User:
		return s.deny(403, "wrong_user", "certificate user does not own this device", ev), nil
	case d.Revoked:
		return s.deny(403, "device_revoked", "device "+id.Device+" was revoked: "+d.RevokedReason+". Re-enroll with `agentbox enroll` after an admin review.", ev), nil
	case d.Attestation != "pass":
		return s.deny(403, "attestation_failed", "appliance attestation is "+d.Attestation+"; model access is off until it passes", ev), nil
	}
	u, ok := st.users[id.User]
	if !ok || u.Suspended {
		return s.deny(403, "user_suspended", "user "+id.User+" is suspended", ev), nil
	}
	if k := st.kills["global"]; k.Enabled {
		return s.deny(503, "kill_switch", "all model access is paused by an administrator ("+k.Reason+")", ev), nil
	}
	if k := st.kills["group:"+u.Group]; k.Enabled {
		return s.deny(503, "kill_switch", "model access for group "+u.Group+" is paused ("+k.Reason+")", ev), nil
	}
	model := ""
	if m := modelRe.FindStringSubmatch(h.GetBody()); m != nil {
		model = m[1]
	}
	ev["gen_ai.request.model"] = model
	if model != "" {
		if k := st.kills["model:"+model]; k.Enabled {
			return s.deny(503, "kill_switch", "model "+model+" is switched off by an administrator ("+k.Reason+"); other models still work", ev), nil
		}
		if allowed := st.models[id.Device]; !contains(allowed, model) {
			return s.deny(403, "model_not_allowed", fmt.Sprintf("model %s is not in your policy (allowed: %s)", model, strings.Join(allowed, ", ")), ev), nil
		}
	}
	// The tier comes from the device's signed bundle: the user's tier plus any tier
	// permission an approver granted, so a granted tier is checked against the
	// device's assurance level like any other.
	tier := u.Tier
	if t := st.tiers[id.Device]; t > 0 {
		tier = t
	}
	if tier > levelCap(d.Level) {
		return s.deny(403, "assurance_level", fmt.Sprintf("tier %d needs assurance level B or higher; this PC is at level %s", tier, d.Level), ev), nil
	}
	if sp := st.spend[id.User]; sp >= u.Budget {
		return s.deny(402, "budget_exceeded", fmt.Sprintf("model budget exhausted for %s: $%.4f used of $%.4f. Ask an admin to raise it in the console.", id.User, sp, u.Budget), ev), nil
	}
	// Key generation outlives the request: if Envoy gives up after its authz
	// timeout, the key is still created and cached for the retry.
	subject := keymint.Subject{User: id.User, Device: id.Device, Sandbox: headers["x-agentbox-sandbox"], Tier: tier, Models: st.models[id.Device]}
	if rem := u.Budget - st.spend[id.User]; rem > 0 {
		subject.MaxBudget = rem
	}
	key, err := s.mint.Key(context.WithoutCancel(ctx), subject)
	if err != nil {
		log.Printf("key for %s/%s: %v", id.User, id.Device, err)
		return s.deny(503, "model_service_unavailable", "model service unreachable, try again shortly", ev), nil
	}
	set := map[string]string{
		"authorization":      "Bearer " + key,
		"x-agentbox-user":    id.User,
		"x-agentbox-device":  id.Device,
		"x-agentbox-tier":    fmt.Sprint(tier),
		"x-agentbox-model":   model,
		"x-agentbox-group":   u.Group,
	}
	return extauthz.Allow(set, "x-api-key"), nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// trusted reports whether a device may be given model keys at all.
func trusted(d device) bool { return !d.Revoked && d.Attestation == "pass" }

func envDur(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if x, err := time.ParseDuration(v); err == nil {
			return x
		}
		log.Fatalf("%s=%q is not a duration such as 15m", k, v)
	}
	return d
}

func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		if _, err := fmt.Sscan(v, &n); err != nil {
			log.Fatalf("%s=%q is not a number", k, v)
		}
		return n
	}
	return d
}
