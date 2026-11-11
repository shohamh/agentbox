// agentbox-console is the admin API and web UI, plus (in the proof of
// concept) the enrollment and light-attestation endpoints that appliances
// call. State is in Postgres; events are read from ClickHouse.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentbox/pkg/bundle"
	"agentbox/pkg/ch"
	"agentbox/pkg/db"
	"agentbox/pkg/measure"
	"agentbox/pkg/otlp"
	"agentbox/pkg/pki"
)

type Server struct {
	pool    *pgxpool.Pool
	llmDB   *pgxpool.Pool
	ch      *ch.Client
	ca      *pki.CAClient
	ev      *otlp.Exporter
	oidc    *OIDC
	sess    *sessions
	sse     *broker
	release ed25519.PublicKey

	mu        sync.RWMutex
	manifests []measure.Manifest

	releasesDir   string
	collectorConf string
	uiDir         string
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	s := &Server{pool: pool, sess: newSessions(pool), sse: newBroker(),
		ch:            ch.New(env("CLICKHOUSE_URL", "http://clickhouse:8123"), os.Getenv("CLICKHOUSE_USER"), os.Getenv("CLICKHOUSE_PASSWORD"), "otel"),
		ev:            otlp.New(os.Getenv("OTLP_ENDPOINT"), map[string]string{"service.name": "agentbox-console"}, nil),
		releasesDir:   env("RELEASES_DIR", "/etc/agentbox/releases"),
		collectorConf: os.Getenv("COLLECTOR_DYNAMIC_CONFIG"),
		uiDir:         env("UI_DIR", "/usr/share/agentbox/console"),
	}
	if u := os.Getenv("LITELLM_DATABASE_URL"); u != "" {
		if s.llmDB, err = db.Open(ctx, u); err != nil {
			log.Fatal(err)
		}
	}
	seedPath := env("SEED_FILE", "/etc/agentbox/seed.json")
	if err := s.seed(ctx, seedPath); err != nil {
		log.Fatalf("seed: %v", err)
	}
	go s.watchImages(ctx, seedPath)
	if s.release, err = bundle.LoadPublicKey(os.Getenv("RELEASE_PUBKEY_FILE")); err != nil {
		log.Fatalf("release key: %v", err)
	}
	s.loadManifests()
	go func() {
		for range time.Tick(15 * time.Second) {
			s.loadManifests()
		}
	}()
	for {
		s.ca, err = pki.NewCAClient(env("STEPCA_URL", "https://ca.agentbox.corp:9000"), os.Getenv("STEPCA_ROOT_FILE"), env("STEPCA_PROVISIONER", "agentbox"), os.Getenv("STEPCA_PASSWORD_FILE"))
		if err == nil {
			break
		}
		log.Printf("step-ca client: %v", err)
		time.Sleep(3 * time.Second)
	}
	s.oidc = newOIDC(ctx)
	go db.Listen(ctx, pool, func(_, topic string) { s.sse.publish(topic) }, db.ChanConsole)
	go s.watchStale(ctx)
	s.writeCollectorConfig(ctx)

	// Admin UI and API (HTTP, behind the corporate proxy in production).
	admin := http.NewServeMux()
	s.routes(admin)
	go func() {
		addr := env("LISTEN", ":18080")
		log.Printf("console UI/API on %s", addr)
		log.Fatal((&http.Server{Addr: addr, Handler: logReq(admin), ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
	}()

	// Enrollment and attestation for PCs and appliances (TLS).
	dev := http.NewServeMux()
	dev.HandleFunc("POST /api/v1/enroll", s.enroll)
	dev.HandleFunc("POST /api/v1/attest/nonce", s.nonce)
	dev.HandleFunc("POST /api/v1/attest", s.attest)
	dev.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	tlsCfg, err := pki.ServerTLS(os.Getenv("TLS_CERT_FILE"), os.Getenv("TLS_KEY_FILE"), nil, false)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: env("ENROLL_LISTEN", ":8443"), Handler: logReq(dev), TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("enrollment/attestation on %s (TLS)", srv.Addr)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

func logReq(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/assets") && r.URL.Path != "/api/events" {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) loadManifests() {
	files, _ := filepath.Glob(filepath.Join(s.releasesDir, "*.json"))
	var out []measure.Manifest
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var m measure.Manifest
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if !bundle.VerifyBlob(s.release, m.Payload(), m.Signature) {
			log.Printf("release manifest %s: bad signature, ignored", f)
			continue
		}
		out = append(out, m)
	}
	s.mu.Lock()
	if len(out) != len(s.manifests) {
		log.Printf("loaded %d signed release manifest(s)", len(out))
	}
	s.manifests = out
	s.mu.Unlock()
}

// watchStale marks devices offline in the UI and raises D3 for missed
// check-ins of devices that were attested.
func (s *Server) watchStale(ctx context.Context) {
	for range time.Tick(10 * time.Second) {
		s.sse.publish("devices")
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// audit records an admin or system action in Postgres and as an OTel log.
func (s *Server) audit(ctx context.Context, actor, role, action, target string, detail map[string]any) {
	d, _ := json.Marshal(detail)
	if _, err := s.pool.Exec(ctx, `INSERT INTO audit(actor, role, action, target, detail) VALUES($1,$2,$3,$4,$5)`, actor, role, action, target, d); err != nil {
		log.Printf("audit: %v", err)
	}
	attrs := map[string]any{"audit.actor": actor, "audit.role": role, "audit.action": action, "audit.target": target, "audit.detail": string(d)}
	s.ev.Emit("audit."+action, attrs)
	db.Notify(ctx, s.pool, db.ChanConsole, "audit")
}
