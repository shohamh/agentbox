package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"agentbox/pkg/bundle"
	"agentbox/pkg/db"
	"agentbox/pkg/policy"
)

func (s *Server) routes(m *http.ServeMux) {
	m.HandleFunc("GET /auth/login", s.login)
	m.HandleFunc("GET /auth/callback", s.callback)
	m.HandleFunc("GET /auth/logout", s.logout)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	m.HandleFunc("GET /api/me", s.need("viewer", func(w http.ResponseWriter, r *http.Request, ss *session) {
		writeJSON(w, 200, map[string]any{"user": ss.User, "email": ss.Email, "role": ss.Role()})
	}))
	m.HandleFunc("GET /api/events", s.need("viewer", s.events))
	m.HandleFunc("GET /api/overview", s.need("viewer", s.overview))

	m.HandleFunc("GET /api/devices", s.need("viewer", s.devices))
	m.HandleFunc("GET /api/devices/{id}", s.need("viewer", s.device))
	m.HandleFunc("POST /api/devices/{id}/revoke", s.need("admin", s.revokeDevice))
	m.HandleFunc("POST /api/devices/{id}/recycle", s.need("admin", s.recycleDevice))
	m.HandleFunc("POST /api/devices/{id}/level", s.need("admin", s.setLevel))

	m.HandleFunc("GET /api/users", s.need("viewer", s.users))
	m.HandleFunc("POST /api/users/{name}", s.need("admin", s.updateUser))

	m.HandleFunc("GET /api/sandboxes", s.need("viewer", s.sandboxes))
	m.HandleFunc("POST /api/sandboxes/{device}/{sb}/{action}", s.need("admin", s.sandboxAction))

	m.HandleFunc("GET /api/requests", s.need("viewer", s.requests))
	m.HandleFunc("POST /api/requests/{id}/{action}", s.need("approver", s.requestAction))

	m.HandleFunc("GET /api/policy", s.need("viewer", s.getPolicy))
	m.HandleFunc("POST /api/policy/preview", s.need("admin", s.previewPolicy))
	m.HandleFunc("PUT /api/policy/groups/{name}", s.need("admin", s.publishGroup))
	m.HandleFunc("PUT /api/policy/catalog", s.need("admin", s.putCatalog))

	m.HandleFunc("GET /api/activity", s.need("viewer", s.activity))
	m.HandleFunc("POST /api/activity/pin", s.need("approver", s.pin))

	m.HandleFunc("GET /api/alerts", s.need("viewer", s.alerts))
	m.HandleFunc("POST /api/alerts/{id}/{action}", s.need("approver", s.alertAction))

	m.HandleFunc("GET /api/kill", s.need("viewer", s.kills))
	m.HandleFunc("POST /api/kill", s.need("admin", s.setKill))

	m.HandleFunc("GET /api/audit", s.need("viewer", s.auditLog))

	m.HandleFunc("GET /api/settings", s.need("viewer", s.getSettings))
	m.HandleFunc("PUT /api/settings/{key}", s.need("admin", s.putSettings))
	m.HandleFunc("POST /api/settings/exporters/test", s.need("admin", s.testExporter))
	m.HandleFunc("GET /api/releases", s.need("viewer", s.releases))

	m.HandleFunc("/", s.static)
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	p := filepath.Join(s.uiDir, filepath.Clean("/"+r.URL.Path))
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, p)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(s.uiDir, "index.html"))
}

func body(r *http.Request) map[string]any {
	m := map[string]any{}
	json.NewDecoder(r.Body).Decode(&m)
	return m
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return strings.TrimSpace(v)
}

// rowsJSON turns any query into a JSON array of objects.
func (s *Server) rowsJSON(ctx context.Context, q string, args ...any) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := pgx.CollectRows(rows, pgx.RowToMap)
	if out == nil {
		out = []map[string]any{}
	}
	return out, err
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, q string, args ...any) {
	out, err := s.rowsJSON(r.Context(), q, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// ---------- overview ----------

func (s *Server) overview(w http.ResponseWriter, r *http.Request, _ *session) {
	ctx := r.Context()
	out := map[string]any{}
	var enrolled, online, revoked, attested int
	s.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE last_seen > now() - interval '60 seconds' AND NOT revoked),
		count(*) FILTER (WHERE revoked), count(*) FILTER (WHERE attestation_status='pass') FROM devices`).Scan(&enrolled, &online, &revoked, &attested)
	var live int
	s.pool.QueryRow(ctx, `SELECT coalesce(sum((SELECT count(*) FROM jsonb_array_elements(coalesce(status->'sandboxes','[]'::jsonb)) e WHERE e->>'state'='running')),0)
		FROM devices WHERE last_seen > now() - interval '60 seconds'`).Scan(&live)
	var openReq, flagged, openAlerts int
	s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='pending'), count(*) FILTER (WHERE review_status IN ('pending','unusual','escalate','sampled') AND status='active') FROM requests`).Scan(&openReq, &flagged)
	s.pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE status IN ('open','escalated')`).Scan(&openAlerts)
	out["devices_enrolled"], out["devices_online"], out["devices_revoked"], out["devices_attested"] = enrolled, online, revoked, attested
	out["sandboxes_live"], out["requests_open"], out["requests_flagged"], out["alerts_open"] = live, openReq, flagged, openAlerts
	rows, err := s.ch.Query(ctx, `SELECT count() AS n FROM otel_logs WHERE LogAttributes['event.name']='model.call' AND Timestamp > now() - INTERVAL 5 MINUTE`, nil)
	if err == nil && len(rows) == 1 {
		n, _ := strconv.ParseFloat(fmt.Sprint(rows[0]["n"]), 64)
		out["model_calls_per_min"] = n / 5
	}
	rows, err = s.ch.Query(ctx, `SELECT toStartOfMinute(Timestamp) AS t, count() AS n FROM otel_logs
		WHERE LogAttributes['event.name']='model.call' AND Timestamp > now() - INTERVAL 30 MINUTE GROUP BY t ORDER BY t`, nil)
	if err == nil {
		out["model_calls_series"] = rows
	}
	var ks []string
	krows, _ := s.pool.Query(ctx, `SELECT scope FROM kill_switches WHERE enabled`)
	for krows != nil && krows.Next() {
		var k string
		krows.Scan(&k)
		ks = append(ks, k)
	}
	if krows != nil {
		krows.Close()
	}
	out["kill_switches_on"] = ks
	writeJSON(w, 200, out)
}

// ---------- devices ----------

const deviceCols = `device_id, username, os, hostname, enrolled_at, revoked, revoked_reason, revoked_by, revoked_at, assurance_level,
  attestation_status, attestation_detail, attested_at, appliance_version, bundle_version, last_seen,
  (last_seen > now() - interval '60 seconds') AS online, status,
  (SELECT version FROM bundles b WHERE b.device_id=d.device_id) AS published_bundle`

func (s *Server) devices(w http.ResponseWriter, r *http.Request, _ *session) {
	s.list(w, r, `SELECT `+deviceCols+` FROM devices d ORDER BY device_id`)
}

func (s *Server) device(w http.ResponseWriter, r *http.Request, _ *session) {
	out, err := s.rowsJSON(r.Context(), `SELECT `+deviceCols+` FROM devices d WHERE device_id=$1`, r.PathValue("id"))
	if err != nil || len(out) == 0 {
		fail(w, 404, "no such device")
		return
	}
	d := out[0]
	var raw []byte
	if s.pool.QueryRow(r.Context(), `SELECT signed FROM bundles WHERE device_id=$1`, r.PathValue("id")).Scan(&raw) == nil {
		var sb bundle.Signed
		json.Unmarshal(raw, &sb)
		if b, err := bundle.Decode(&sb); err == nil {
			d["bundle"] = b
		}
	}
	writeJSON(w, 200, d)
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request, ss *session) {
	b := body(r)
	reason := str(b, "reason")
	if reason == "" {
		fail(w, 400, "a reason is required")
		return
	}
	id := r.PathValue("id")
	tag, err := s.pool.Exec(r.Context(), `UPDATE devices SET revoked=true, revoked_reason=$2, revoked_by=$3, revoked_at=now(), attestation_status='revoked' WHERE device_id=$1`, id, reason, ss.User)
	if err != nil || tag.RowsAffected() == 0 {
		fail(w, 404, "no such device")
		return
	}
	s.audit(r.Context(), ss.User, ss.Role(), "device.revoke", id, map[string]any{"reason": reason})
	db.Notify(r.Context(), s.pool, db.ChanCommands, id)
	db.Notify(r.Context(), s.pool, db.ChanConsole, "devices")
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) command(ctx context.Context, device, kind string, args map[string]any, by, reason string) error {
	a, _ := json.Marshal(args)
	_, err := s.pool.Exec(ctx, `INSERT INTO commands(device_id, kind, args, created_by, reason) VALUES($1,$2,$3,$4,$5)`, device, kind, a, by, reason)
	if err == nil {
		db.Notify(ctx, s.pool, db.ChanCommands, device)
	}
	return err
}

func (s *Server) recycleDevice(w http.ResponseWriter, r *http.Request, ss *session) {
	b := body(r)
	id := r.PathValue("id")
	if err := s.command(r.Context(), id, "recycle_appliance", nil, ss.User, str(b, "reason")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), ss.User, ss.Role(), "device.recycle", id, map[string]any{"reason": str(b, "reason")})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) setLevel(w http.ResponseWriter, r *http.Request, ss *session) {
	b := body(r)
	lvl := str(b, "level")
	if lvl != "A" && lvl != "B" && lvl != "C" {
		fail(w, 400, "level must be A, B or C")
		return
	}
	id := r.PathValue("id")
	s.pool.Exec(r.Context(), `UPDATE devices SET assurance_level=$2 WHERE device_id=$1`, id, lvl)
	s.audit(r.Context(), ss.User, ss.Role(), "device.level", id, map[string]any{"level": lvl})
	db.Notify(r.Context(), s.pool, db.ChanPolicy, id)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- users ----------

func (s *Server) users(w http.ResponseWriter, r *http.Request, _ *session) {
	out, err := s.rowsJSON(r.Context(), `SELECT u.username, u.email, u.grp, u.tier, u.budget_usd::float8 AS budget_usd, u.suspended,
		(SELECT array_agg(device_id) FROM devices d WHERE d.username=u.username) AS devices FROM users u ORDER BY username`)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	spend := map[string]float64{}
	if s.llmDB != nil {
		rows, err := s.llmDB.Query(r.Context(), `SELECT user_id, coalesce(spend,0) FROM "LiteLLM_UserTable"`)
		if err == nil {
			for rows.Next() {
				var u string
				var sp float64
				rows.Scan(&u, &sp)
				spend[u] = sp
			}
			rows.Close()
		}
	}
	for _, u := range out {
		u["spend_usd"] = spend[fmt.Sprint(u["username"])]
	}
	writeJSON(w, 200, out)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, ss *session) {
	b := body(r)
	name := r.PathValue("name")
	ctx := r.Context()
	if v, ok := b["tier"].(float64); ok {
		s.pool.Exec(ctx, `UPDATE users SET tier=$2 WHERE username=$1`, name, int(v))
	}
	if v, ok := b["budget_usd"].(float64); ok {
		s.pool.Exec(ctx, `UPDATE users SET budget_usd=$2 WHERE username=$1`, name, v)
	}
	if v, ok := b["suspended"].(bool); ok {
		s.pool.Exec(ctx, `UPDATE users SET suspended=$2 WHERE username=$1`, name, v)
	}
	if v := str(b, "group"); v != "" {
		s.pool.Exec(ctx, `UPDATE users SET grp=$2 WHERE username=$1`, name, v)
	}
	delete(b, "reason")
	s.audit(ctx, ss.User, ss.Role(), "user.update", name, b)
	db.Notify(ctx, s.pool, db.ChanPolicy, "user:"+name)
	db.Notify(ctx, s.pool, db.ChanConsole, "users")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- sandboxes ----------

func (s *Server) sandboxes(w http.ResponseWriter, r *http.Request, _ *session) {
	s.list(w, r, `SELECT d.device_id, d.username, (d.last_seen > now() - interval '60 seconds') AS online, e AS sandbox
		FROM devices d, jsonb_array_elements(coalesce(d.status->'sandboxes','[]'::jsonb)) e ORDER BY d.device_id, e->>'id'`)
}

func (s *Server) sandboxAction(w http.ResponseWriter, r *http.Request, ss *session) {
	b := body(r)
	reason := str(b, "reason")
	if reason == "" {
		fail(w, 400, "a reason is required")
		return
	}
	kind := map[string]string{"stop": "stop_sandbox", "recycle": "recycle_sandbox"}[r.PathValue("action")]
	if kind == "" {
		fail(w, 400, "action must be stop or recycle")
		return
	}
	dev, sb := r.PathValue("device"), r.PathValue("sb")
	if err := s.command(r.Context(), dev, kind, map[string]any{"sandbox": sb}, ss.User, reason); err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), ss.User, ss.Role(), "sandbox."+r.PathValue("action"), dev+"/"+sb, map[string]any{"reason": reason})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- requests ----------

func (s *Server) requests(w http.ResponseWriter, r *http.Request, _ *session) {
	st := r.URL.Query().Get("status")
	s.list(w, r, `SELECT * FROM requests WHERE ($1='' OR status=$1 OR ($1='flagged' AND class='flagged')) ORDER BY id DESC LIMIT 500`, st)
}

func (s *Server) requestAction(w http.ResponseWriter, r *http.Request, ss *session) {
	ctx := r.Context()
	b := body(r)
	id := r.PathValue("id")
	action := r.PathValue("action")
	reason := str(b, "reason")
	var user, perm, status, class string
	if err := s.pool.QueryRow(ctx, `SELECT username, permission, status, class FROM requests WHERE id=$1`, id).Scan(&user, &perm, &status, &class); err != nil {
		fail(w, 404, "no such request")
		return
	}
	switch action {
	case "approve":
		if status != "pending" {
			fail(w, 409, "request is "+status)
			return
		}
		var exp *time.Time
		if h, ok := b["expires_hours"].(float64); ok && h > 0 {
			t := time.Now().Add(time.Duration(h * float64(time.Hour)))
			exp = &t
		}
		s.pool.Exec(ctx, `UPDATE requests SET status='approved', decided_by=$2, decision_reason=$3, decided_at=now(), expires_at=$4 WHERE id=$1`, id, ss.User, reason, exp)
	case "deny":
		if status != "pending" {
			fail(w, 409, "request is "+status)
			return
		}
		if reason == "" {
			fail(w, 400, "a reason is required to deny")
			return
		}
		s.pool.Exec(ctx, `UPDATE requests SET status='denied', decided_by=$2, decision_reason=$3, decided_at=now() WHERE id=$1`, id, ss.User, reason)
	case "rule":
		if ss.Role() != "admin" {
			fail(w, 403, "admin role required to create automatic rules")
			return
		}
		pattern := str(b, "pattern")
		if pattern == "" {
			pattern = perm
		}
		s.pool.Exec(ctx, `INSERT INTO auto_rules(pattern, created_by) VALUES($1,$2) ON CONFLICT DO NOTHING`, pattern, ss.User)
		if status == "pending" {
			s.pool.Exec(ctx, `UPDATE requests SET status='approved', decided_by=$2, decision_reason='turned into an automatic rule', decided_at=now() WHERE id=$1`, id, ss.User)
		}
	case "confirm":
		s.pool.Exec(ctx, `UPDATE requests SET review_status='confirmed', decided_by=$2, decision_reason=$3 WHERE id=$1`, id, ss.User, reason)
	case "rollback":
		if status != "active" && status != "approved" {
			fail(w, 409, "only active grants can be rolled back")
			return
		}
		s.pool.Exec(ctx, `UPDATE requests SET status='rolled_back', review_status='rolled_back', decided_by=$2, decision_reason=$3, decided_at=now() WHERE id=$1`, id, ss.User, reason)
	default:
		fail(w, 400, "unknown action")
		return
	}
	s.audit(ctx, ss.User, ss.Role(), "request."+action, perm, map[string]any{"request_id": id, "user": user, "reason": reason, "class": class})
	db.Notify(ctx, s.pool, db.ChanPolicy, "user:"+user)
	db.Notify(ctx, s.pool, db.ChanConsole, "requests")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- policy ----------

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request, _ *session) {
	groups, err := s.rowsJSON(r.Context(), `SELECT name, policy, updated_at, updated_by FROM groups ORDER BY name`)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	var cat policy.Catalog
	db.GetSetting(r.Context(), s.pool, "catalog", &cat)
	rules, _ := s.rowsJSON(r.Context(), `SELECT * FROM auto_rules ORDER BY id`)
	writeJSON(w, 200, map[string]any{"groups": groups, "catalog": cat, "auto_rules": rules})
}

type devDiff struct {
	Device  string   `json:"device"`
	User    string   `json:"user"`
	Changes []string `json:"changes"`
}

func diffBundles(a, b *bundle.Bundle) []string {
	var out []string
	set := func(l []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range l {
			m[x] = true
		}
		return m
	}
	cmp := func(name string, x, y []string) {
		mx, my := set(x), set(y)
		for k := range my {
			if !mx[k] {
				out = append(out, "+ "+name+" "+k)
			}
		}
		for k := range mx {
			if !my[k] {
				out = append(out, "- "+name+" "+k)
			}
		}
	}
	cmp("model", a.Models, b.Models)
	cmp("egress", a.Egress, b.Egress)
	var ai, bi []string
	for k := range a.Images {
		ai = append(ai, k)
	}
	for k := range b.Images {
		bi = append(bi, k)
	}
	cmp("image", ai, bi)
	if a.Limits != b.Limits {
		out = append(out, fmt.Sprintf("~ limits %+v -> %+v", a.Limits, b.Limits))
	}
	if a.RateLimitRPM != b.RateLimitRPM {
		out = append(out, fmt.Sprintf("~ rate limit %d -> %d rpm", a.RateLimitRPM, b.RateLimitRPM))
	}
	return out
}

func (s *Server) previewPolicy(w http.ResponseWriter, r *http.Request, _ *session) {
	var in struct {
		Group  string             `json:"group"`
		Policy policy.GroupPolicy `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, s.diffForGroup(r.Context(), in.Group, in.Policy))
}

func (s *Server) diffForGroup(ctx context.Context, group string, newPol policy.GroupPolicy) []devDiff {
	rows, err := s.pool.Query(ctx, `SELECT d.device_id, d.username, b.signed FROM devices d JOIN users u ON u.username=d.username
		LEFT JOIN bundles b ON b.device_id=d.device_id WHERE u.grp=$1 AND NOT d.revoked`, group)
	out := []devDiff{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var dev, user string
		var raw []byte
		rows.Scan(&dev, &user, &raw)
		cur := &bundle.Bundle{}
		if raw != nil {
			var sb bundle.Signed
			json.Unmarshal(raw, &sb)
			if b, err := bundle.Decode(&sb); err == nil {
				cur = b
			}
		}
		// Recompute as the permission service will: new group defaults plus grants.
		next := *cur
		next.Models, next.Egress, next.Limits, next.RateLimitRPM = append([]string{}, newPol.Models...), append([]string{}, newPol.Egress...), newPol.Limits, newPol.RateLimitRPM
		var cat policy.Catalog
		db.GetSetting(ctx, s.pool, "catalog", &cat)
		next.Images = map[string]bundle.ImageRef{}
		for _, n := range newPol.Images {
			if ref, ok := cat.Images[n]; ok {
				next.Images[n] = ref
			}
		}
		for _, g := range cur.Grants {
			k, v, _ := strings.Cut(g.Permission, ":")
			switch k {
			case "egress":
				next.Egress = append(next.Egress, v)
			case "model":
				next.Models = append(next.Models, v)
			case "image":
				if ref, ok := cat.Images[v]; ok {
					next.Images[v] = ref
				}
			}
		}
		out = append(out, devDiff{Device: dev, User: user, Changes: diffBundles(cur, &next)})
	}
	return out
}

func (s *Server) publishGroup(w http.ResponseWriter, r *http.Request, ss *session) {
	var in struct {
		Policy policy.GroupPolicy `json:"policy"`
		Reason string             `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	name := r.PathValue("name")
	diff := s.diffForGroup(r.Context(), name, in.Policy)
	pb, _ := json.Marshal(in.Policy)
	_, err := s.pool.Exec(r.Context(), `INSERT INTO groups(name, policy, updated_by) VALUES($1,$2,$3)
		ON CONFLICT(name) DO UPDATE SET policy=$2, updated_by=$3, updated_at=now()`, name, pb, ss.User)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), ss.User, ss.Role(), "policy.publish", "group:"+name, map[string]any{"policy": in.Policy, "reason": in.Reason, "devices": len(diff)})
	db.Notify(r.Context(), s.pool, db.ChanPolicy, "*")
	writeJSON(w, 200, map[string]any{"ok": true, "diff": diff})
}

func (s *Server) putCatalog(w http.ResponseWriter, r *http.Request, ss *session) {
	var cat policy.Catalog
	if err := json.NewDecoder(r.Body).Decode(&cat); err != nil {
		fail(w, 400, err.Error())
		return
	}
	db.PutSetting(r.Context(), s.pool, "catalog", cat, ss.User)
	s.audit(r.Context(), ss.User, ss.Role(), "policy.catalog", "catalog", map[string]any{"egress": cat.Egress, "images": len(cat.Images)})
	db.Notify(r.Context(), s.pool, db.ChanPolicy, "*")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- activity (ClickHouse) ----------

func (s *Server) activity(w http.ResponseWriter, r *http.Request, _ *session) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	sql := `SELECT toString(Timestamp) AS ts, Body AS body, SeverityText AS severity, ServiceName AS service,
	  LogAttributes['event.name'] AS event,
	  coalesce(nullIf(LogAttributes['agentbox.sandbox'], ''), ResourceAttributes['agentbox.sandbox']) AS sandbox,
	  coalesce(nullIf(ResourceAttributes['agentbox.user'], ''), LogAttributes['agentbox.user']) AS user,
	  coalesce(nullIf(ResourceAttributes['agentbox.device'], ''), LogAttributes['agentbox.device']) AS device,
	  TraceId AS trace_id, LogAttributes AS attrs
	FROM otel_logs WHERE Timestamp > now() - INTERVAL {hours:UInt32} HOUR`
	params := map[string]string{"hours": "24"}
	if h := q.Get("hours"); h != "" {
		params["hours"] = h
	}
	if sb := q.Get("sandbox"); sb != "" {
		sql += ` AND sandbox = {sb:String}`
		params["sb"] = sb
	}
	if d := q.Get("device"); d != "" {
		sql += ` AND device = {dev:String}`
		params["dev"] = d
	}
	if u := q.Get("user"); u != "" {
		sql += ` AND user = {usr:String}`
		params["usr"] = u
	}
	if k := q.Get("kinds"); k != "" {
		var conds []string
		for i, p := range strings.Split(k, ",") {
			key := fmt.Sprintf("k%d", i)
			conds = append(conds, fmt.Sprintf("startsWith(coalesce(nullIf(event,''), body), {%s:String})", key))
			params[key] = strings.TrimSpace(p)
		}
		sql += " AND (" + strings.Join(conds, " OR ") + ")"
	}
	if text := q.Get("q"); text != "" {
		sql += ` AND (positionCaseInsensitive(Body, {q:String}) > 0 OR positionCaseInsensitive(toString(LogAttributes), {q:String}) > 0)`
		params["q"] = text
	}
	sql += fmt.Sprintf(" ORDER BY Timestamp DESC LIMIT %d", limit)
	rows, err := s.ch.Query(r.Context(), sql, params)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=activity.csv")
		cw := csv.NewWriter(w)
		cw.Write([]string{"ts", "event", "user", "device", "sandbox", "attrs"})
		for _, row := range rows {
			a, _ := json.Marshal(row["attrs"])
			cw.Write([]string{fmt.Sprint(row["ts"]), fmt.Sprint(row["event"]), fmt.Sprint(row["user"]), fmt.Sprint(row["device"]), fmt.Sprint(row["sandbox"]), string(a)})
		}
		cw.Flush()
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) pin(w http.ResponseWriter, r *http.Request, ss *session) {
	b := body(r)
	ev, _ := json.Marshal(b["event"])
	s.pool.Exec(r.Context(), `INSERT INTO evidence_pins(sandbox, event, note, pinned_by) VALUES($1,$2,$3,$4)`, str(b, "sandbox"), ev, str(b, "note"), ss.User)
	s.audit(r.Context(), ss.User, ss.Role(), "evidence.pin", str(b, "sandbox"), map[string]any{"note": str(b, "note")})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- alerts ----------

func (s *Server) alerts(w http.ResponseWriter, r *http.Request, _ *session) {
	s.list(w, r, `SELECT * FROM alerts WHERE ($1='' OR status=$1) ORDER BY id DESC LIMIT 500`, r.URL.Query().Get("status"))
}

func (s *Server) alertAction(w http.ResponseWriter, r *http.Request, ss *session) {
	ctx := r.Context()
	b := body(r)
	id := r.PathValue("id")
	var dev string
	if err := s.pool.QueryRow(ctx, `SELECT device_id FROM alerts WHERE id=$1`, id).Scan(&dev); err != nil {
		fail(w, 404, "no such alert")
		return
	}
	switch a := r.PathValue("action"); a {
	case "acknowledge":
		s.pool.Exec(ctx, `UPDATE alerts SET status='acknowledged', updated_at=now() WHERE id=$1`, id)
	case "close":
		s.pool.Exec(ctx, `UPDATE alerts SET status='closed', updated_at=now() WHERE id=$1`, id)
	case "escalate":
		s.pool.Exec(ctx, `UPDATE alerts SET status='escalated', updated_at=now() WHERE id=$1`, id)
	case "assign":
		s.pool.Exec(ctx, `UPDATE alerts SET assignee=$2, updated_at=now() WHERE id=$1`, id, str(b, "assignee"))
	case "revoke":
		if ss.Role() != "admin" {
			fail(w, 403, "admin role required to revoke")
			return
		}
		reason := "revoked from alert " + id
		s.pool.Exec(ctx, `UPDATE devices SET revoked=true, revoked_reason=$2, revoked_by=$3, revoked_at=now(), attestation_status='revoked' WHERE device_id=$1`, dev, reason, ss.User)
		s.pool.Exec(ctx, `UPDATE alerts SET status='closed', updated_at=now() WHERE id=$1`, id)
		db.Notify(ctx, s.pool, db.ChanCommands, dev)
		db.Notify(ctx, s.pool, db.ChanConsole, "devices")
	default:
		fail(w, 400, "unknown action")
		return
	}
	s.audit(ctx, ss.User, ss.Role(), "alert."+r.PathValue("action"), id, b)
	db.Notify(ctx, s.pool, db.ChanConsole, "alerts")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- kill switches ----------

func (s *Server) kills(w http.ResponseWriter, r *http.Request, _ *session) {
	s.list(w, r, `SELECT * FROM kill_switches ORDER BY scope`)
}

// setKill toggles a switch with a required reason. Turning the global switch
// on (all model access off) needs a second admin to confirm.
func (s *Server) setKill(w http.ResponseWriter, r *http.Request, ss *session) {
	ctx := r.Context()
	b := body(r)
	scope, reason := str(b, "scope"), str(b, "reason")
	enabled, _ := b["enabled"].(bool)
	if reason == "" {
		fail(w, 400, "a reason is required")
		return
	}
	if scope != "global" && !strings.HasPrefix(scope, "model:") && !strings.HasPrefix(scope, "group:") {
		fail(w, 400, "scope must be global, model:<name> or group:<name>")
		return
	}
	s.pool.Exec(ctx, `INSERT INTO kill_switches(scope) VALUES($1) ON CONFLICT DO NOTHING`, scope)
	if scope == "global" && enabled {
		var pendingBy string
		var pendingAt *time.Time
		s.pool.QueryRow(ctx, `SELECT pending_by, pending_at FROM kill_switches WHERE scope='global'`).Scan(&pendingBy, &pendingAt)
		if pendingBy == "" || pendingAt == nil || time.Since(*pendingAt) > 15*time.Minute {
			s.pool.Exec(ctx, `UPDATE kill_switches SET pending_by=$1, pending_reason=$2, pending_at=now() WHERE scope='global'`, ss.User, reason)
			s.audit(ctx, ss.User, ss.Role(), "kill.request", scope, map[string]any{"reason": reason})
			db.Notify(ctx, s.pool, db.ChanConsole, "kill")
			writeJSON(w, 202, map[string]any{"ok": true, "pending": true, "message": "a second admin must confirm turning off all model access"})
			return
		}
		if pendingBy == ss.User {
			fail(w, 409, "the global switch needs a different admin to confirm")
			return
		}
		reason = fmt.Sprintf("%s (requested by %s, confirmed by %s)", reason, pendingBy, ss.User)
	}
	_, err := s.pool.Exec(ctx, `UPDATE kill_switches SET enabled=$2, reason=$3, set_by=$4, set_at=now(), pending_by='', pending_reason='', pending_at=NULL WHERE scope=$1`, scope, enabled, reason, ss.User)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.audit(ctx, ss.User, ss.Role(), map[bool]string{true: "kill.on", false: "kill.off"}[enabled], scope, map[string]any{"reason": reason})
	db.Notify(ctx, s.pool, db.ChanConsole, "kill")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- audit ----------

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request, _ *session) {
	q := r.URL.Query().Get("q")
	rows, err := s.rowsJSON(r.Context(), `SELECT * FROM audit WHERE ($1='' OR actor ILIKE '%'||$1||'%' OR action ILIKE '%'||$1||'%' OR target ILIKE '%'||$1||'%' OR detail::text ILIKE '%'||$1||'%')
		ORDER BY id DESC LIMIT 1000`, q)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=audit.csv")
		cw := csv.NewWriter(w)
		cw.Write([]string{"id", "at", "actor", "role", "action", "target", "detail"})
		for _, a := range rows {
			d, _ := json.Marshal(a["detail"])
			cw.Write([]string{fmt.Sprint(a["id"]), fmt.Sprint(a["at"]), fmt.Sprint(a["actor"]), fmt.Sprint(a["role"]), fmt.Sprint(a["action"]), fmt.Sprint(a["target"]), string(d)})
		}
		cw.Flush()
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) releases(w http.ResponseWriter, r *http.Request, _ *session) {
	s.mu.RLock()
	m := s.manifests
	s.mu.RUnlock()
	var revoked []string
	db.GetSetting(r.Context(), s.pool, "revoked_releases", &revoked)
	writeJSON(w, 200, map[string]any{"manifests": m, "revoked": revoked})
}
