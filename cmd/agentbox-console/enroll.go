package main

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"agentbox/pkg/bundle"
	"agentbox/pkg/db"
	"agentbox/pkg/identity"
	"agentbox/pkg/measure"
	"agentbox/pkg/policy"
	"agentbox/pkg/tpm"
)

const (
	deviceCertTTL    = 30 * 24 * time.Hour
	applianceCertTTL = time.Hour
)

type enrollReq struct {
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
	Hostname    string `json:"hostname"`
	OS          string `json:"os"`
	CSR         string `json:"csr"`
	AKPublic    []byte `json:"ak_public"`
	EKPublic    []byte `json:"ek_public"`
	SSHPublic   string `json:"ssh_public"`
}

// enroll accepts a PC only if it is in the (Fleet) inventory, assigned to the
// signed-in user, and issues its device certificate from step-ca.
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var in enrollReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	c, err := s.oidc.verifyAccess(ctx, in.AccessToken)
	if err != nil {
		fail(w, 401, "sign-in token invalid: "+err.Error())
		return
	}
	if c.Azp != "agentbox-cli" {
		fail(w, 401, "token was not issued to the agentbox CLI")
		return
	}
	var assigned string
	if err := s.pool.QueryRow(ctx, `SELECT assigned_user FROM inventory WHERE device_id=$1`, in.DeviceID).Scan(&assigned); err != nil {
		s.audit(ctx, c.Username, "user", "device.enroll_refused", in.DeviceID, map[string]any{"why": "not in inventory"})
		fail(w, 403, fmt.Sprintf("device %s is not in the Fleet inventory; ask IT to register it", in.DeviceID))
		return
	}
	if assigned != c.Username {
		s.audit(ctx, c.Username, "user", "device.enroll_refused", in.DeviceID, map[string]any{"why": "assigned to " + assigned})
		fail(w, 403, fmt.Sprintf("device %s is assigned to another user in the inventory", in.DeviceID))
		return
	}
	if len(in.AKPublic) == 0 {
		fail(w, 400, "appliance attestation key missing")
		return
	}
	if _, err := tpm.AKKey(in.AKPublic); err != nil {
		fail(w, 400, "appliance attestation key invalid: "+err.Error())
		return
	}
	// step-ca requires the CSR's SANs to equal the token's, so the device
	// certificate carries the SANs the host put in its CSR; the device-to-user
	// binding lives in the database (the appliance certificate, issued after
	// attestation, is the one that carries the user identity).
	sans, err := csrSANs(in.CSR, in.DeviceID)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	chain, err := s.ca.Sign([]byte(in.CSR), in.DeviceID, sans, deviceCertTTL)
	if err != nil {
		log.Printf("enroll sign: %v", err)
		fail(w, 502, "certificate authority: "+err.Error())
		return
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO users(username, email) VALUES($1,$2) ON CONFLICT(username) DO UPDATE SET email=EXCLUDED.email`, c.Username, c.Email)
	if err == nil {
		_, err = s.pool.Exec(ctx, `INSERT INTO devices(device_id, username, os, hostname, ak_public, ek_public, ssh_public)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(device_id) DO UPDATE SET username=$2, os=$3, hostname=$4, ak_public=$5, ek_public=$6, ssh_public=$7,
			  enrolled_at=now(), revoked=false, revoked_reason='', revoked_by='', revoked_at=NULL,
			  attestation_status='pending', attestation_detail='{}'`,
			in.DeviceID, c.Username, in.OS, in.Hostname, in.AKPublic, in.EKPublic, in.SSHPublic)
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.audit(ctx, c.Username, "user", "device.enrolled", in.DeviceID, map[string]any{"os": in.OS, "hostname": in.Hostname})
	db.Notify(ctx, s.pool, db.ChanPolicy, in.DeviceID)
	db.Notify(ctx, s.pool, db.ChanConsole, "devices")
	writeJSON(w, 200, map[string]any{
		"user": c.Username, "device_id": in.DeviceID, "device_cert": string(chain), "ca_root": string(s.ca.RootPEM),
	})
}

func (s *Server) nonce(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID string `json:"device_id"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	n := randHex(32)
	if _, err := s.pool.Exec(r.Context(), `INSERT INTO nonces(nonce, device_id) VALUES($1,$2)`, n, in.DeviceID); err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.pool.Exec(r.Context(), `DELETE FROM nonces WHERE created_at < now() - interval '10 minutes'`)
	writeJSON(w, 200, map[string]string{"nonce": n})
}

type attestReq struct {
	DeviceID string          `json:"device_id"`
	Nonce    string          `json:"nonce"`
	Quote    tpm.Quote       `json:"quote"`
	EventLog []measure.Entry `json:"event_log"`
	CSR      string          `json:"csr"`
	Version  string          `json:"version"`
}

// attest is the proof of concept's light attestation: a virtual-TPM quote of
// PCR 11 over a fresh nonce, checked against signed release manifests.
func (s *Server) attest(w http.ResponseWriter, r *http.Request) {
	var in attestReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	var user string
	var ak []byte
	var revoked bool
	var revReason string
	err := s.pool.QueryRow(ctx, `SELECT username, ak_public, revoked, revoked_reason FROM devices WHERE device_id=$1`, in.DeviceID).Scan(&user, &ak, &revoked, &revReason)
	if err != nil {
		fail(w, 403, "device not enrolled; run agentbox enroll")
		return
	}
	if revoked {
		fail(w, 403, "device revoked: "+revReason)
		return
	}
	tag, _ := s.pool.Exec(ctx, `DELETE FROM nonces WHERE nonce=$1 AND device_id=$2 AND created_at > now() - interval '2 minutes'`, in.Nonce, in.DeviceID)
	if tag.RowsAffected() != 1 {
		s.attestFail(ctx, in, user, "nonce unknown, reused or expired (replay)", nil)
		fail(w, 403, "attestation failed: nonce unknown, reused or expired")
		return
	}
	nonce, _ := hex.DecodeString(in.Nonce)
	pcr, err := tpm.VerifyQuote(&in.Quote, ak, nonce)
	if err != nil {
		s.attestFail(ctx, in, user, err.Error(), nil)
		fail(w, 403, "attestation failed: "+err.Error())
		return
	}
	pcrHex := hex.EncodeToString(pcr)
	if measure.ExpectedPCR(in.EventLog) != pcrHex {
		s.attestFail(ctx, in, user, "event log does not replay to the quoted PCR 11", nil)
		fail(w, 403, "attestation failed: event log does not match the quote")
		return
	}
	s.mu.RLock()
	manifests := s.manifests
	s.mu.RUnlock()
	var match *measure.Manifest
	for i := range manifests {
		if manifests[i].PCR11 == pcrHex && manifests[i].Flavor == "prod" && !s.releaseRevoked(ctx, manifests[i].Version) {
			match = &manifests[i]
		}
	}
	if match == nil {
		var diff []string
		var latest *measure.Manifest
		for i := range manifests {
			if latest == nil || manifests[i].Created.After(latest.Created) {
				latest = &manifests[i]
			}
		}
		why := "PCR 11 matches no published production release"
		if latest != nil {
			diff = measure.Diff(latest.Entries, in.EventLog)
			for _, m := range manifests {
				if m.PCR11 == pcrHex && m.Flavor != "prod" {
					why = "appliance booted the " + m.Flavor + " flavor, which production never accepts"
				}
				if m.PCR11 == pcrHex && s.releaseRevoked(ctx, m.Version) {
					why = "appliance release " + m.Version + " has been revoked"
				}
			}
		}
		s.attestFail(ctx, in, user, why, diff)
		fail(w, 403, "attestation failed: "+why)
		return
	}
	chain, err := s.ca.Sign([]byte(in.CSR), in.DeviceID+"-appliance", []string{identity.ApplianceID(in.DeviceID, user)}, applianceCertTTL)
	if err != nil {
		fail(w, 502, "certificate authority: "+err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"result": "pass", "pcr11": pcrHex, "release": match.Version, "nonce": in.Nonce, "entries": len(in.EventLog)})
	var prev string
	s.pool.QueryRow(ctx, `SELECT attestation_status FROM devices WHERE device_id=$1`, in.DeviceID).Scan(&prev)
	s.pool.Exec(ctx, `UPDATE devices SET attestation_status='pass', attestation_detail=$2, attested_at=now(), appliance_version=$3 WHERE device_id=$1`, in.DeviceID, detail, match.Version)
	s.ev.Emit("attestation.result", map[string]any{"agentbox.device": in.DeviceID, "agentbox.user": user, "result": "pass", "pcr11": pcrHex, "release": match.Version})
	if prev != "pass" {
		s.audit(ctx, "system", "verifier", "attestation.pass", in.DeviceID, map[string]any{"release": match.Version})
		db.Notify(ctx, s.pool, db.ChanPolicy, in.DeviceID)
	}
	db.Notify(ctx, s.pool, db.ChanConsole, "devices")
	writeJSON(w, 200, map[string]any{"result": "pass", "certificate": string(chain), "ca_root": string(s.ca.RootPEM), "release": match.Version, "ttl_seconds": int(applianceCertTTL.Seconds())})
}

func (s *Server) releaseRevoked(ctx context.Context, version string) bool {
	var revoked []string
	db.GetSetting(ctx, s.pool, "revoked_releases", &revoked)
	for _, v := range revoked {
		if v == version {
			return true
		}
	}
	return false
}

func (s *Server) attestFail(ctx context.Context, in attestReq, user, why string, diff []string) {
	log.Printf("attestation FAILED for %s: %s %v", in.DeviceID, why, diff)
	detail, _ := json.Marshal(map[string]any{"result": "fail", "reason": why, "diff": diff, "pcr11": hex.EncodeToString(in.Quote.PCRValue), "event_log": in.EventLog})
	s.pool.Exec(ctx, `UPDATE devices SET attestation_status='fail', attestation_detail=$2, attested_at=now() WHERE device_id=$1`, in.DeviceID, detail)
	ev, _ := json.Marshal(map[string]any{"reason": why, "diff": diff})
	s.pool.Exec(ctx, `INSERT INTO alerts(detection, severity, device_id, username, title, evidence, dedup_key)
		VALUES('D3','high',$1,$2,$3,$4,$5) ON CONFLICT(dedup_key) DO NOTHING`,
		in.DeviceID, user, "Attestation failed: "+why, ev, fmt.Sprintf("D3:%s:%s", in.DeviceID, time.Now().Format("2006010215")))
	s.ev.EmitSev("WARN", "attestation.result", map[string]any{"agentbox.device": in.DeviceID, "agentbox.user": user, "result": "fail", "reason": why, "diff": strings.Join(diff, "; ")})
	s.audit(ctx, "system", "verifier", "attestation.fail", in.DeviceID, map[string]any{"reason": why, "diff": diff})
	db.Notify(ctx, s.pool, db.ChanConsole, "devices")
	db.Notify(ctx, s.pool, db.ChanConsole, "alerts")
}

// ---------- seed ----------

type seedFile struct {
	Inventory []struct {
		Device string `json:"device"`
		User   string `json:"user"`
		OS     string `json:"os"`
	} `json:"inventory"`
	Users []struct {
		Name   string  `json:"name"`
		Email  string  `json:"email"`
		Group  string  `json:"group"`
		Tier   int     `json:"tier"`
		Budget float64 `json:"budget_usd"`
	} `json:"users"`
	Groups  map[string]policy.GroupPolicy `json:"groups"`
	Catalog policy.Catalog                `json:"catalog"`
	Images  string                        `json:"images_file"`
}

// seed loads inventory, users, group defaults and catalogs on first start.
// Image digests come from the file the image publish step writes.
func (s *Server) seed(ctx context.Context, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("no seed file %s: %v", path, err)
		return nil
	}
	var sf seedFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return err
	}
	for _, i := range sf.Inventory {
		s.pool.Exec(ctx, `INSERT INTO inventory(device_id, assigned_user, os) VALUES($1,$2,$3) ON CONFLICT(device_id) DO UPDATE SET assigned_user=$2, os=$3`, i.Device, i.User, i.OS)
	}
	for _, u := range sf.Users {
		s.pool.Exec(ctx, `INSERT INTO users(username, email, grp, tier, budget_usd) VALUES($1,$2,$3,$4,$5) ON CONFLICT(username) DO NOTHING`, u.Name, u.Email, u.Group, u.Tier, u.Budget)
	}
	for name, g := range sf.Groups {
		gb, _ := json.Marshal(g)
		s.pool.Exec(ctx, `INSERT INTO groups(name, policy) VALUES($1,$2) ON CONFLICT(name) DO NOTHING`, name, gb)
	}
	var existing policy.Catalog
	have := db.GetSetting(ctx, s.pool, "catalog", &existing)
	// The image publish step drops images.json (name -> {ref,digest,harness})
	// into the container or the releases volume, possibly after we started.
	imagesLoaded := false
	for _, p := range imageFiles(sf) {
		if ib, err := os.ReadFile(p); err == nil {
			imgs := map[string]bundle.ImageRef{}
			if err := json.Unmarshal(ib, &imgs); err != nil {
				log.Printf("images file %s: %v", p, err)
				continue
			}
			sf.Catalog.Images, imagesLoaded = imgs, true
			break
		}
	}
	switch {
	case !have:
		db.PutSetting(ctx, s.pool, "catalog", sf.Catalog, "seed")
	case imagesLoaded && imagesChanged(existing, sf.Catalog):
		// keep admin edits to egress and project roots; refresh image digests
		existing.Images = sf.Catalog.Images
		db.PutSetting(ctx, s.pool, "catalog", existing, "seed")
		log.Printf("image catalog updated from images file (%d images)", len(existing.Images))
	}
	for _, k := range []string{"global"} {
		s.pool.Exec(ctx, `INSERT INTO kill_switches(scope) VALUES($1) ON CONFLICT DO NOTHING`, k)
	}
	var rv map[string]any
	if !db.GetSetting(ctx, s.pool, "reviewer", &rv) {
		db.PutSetting(ctx, s.pool, "reviewer", map[string]any{"model": "glm-4", "agent_family": "deepseek", "sample_rate": 0.05}, "seed")
	}
	if !db.GetSetting(ctx, s.pool, "retention", &rv) {
		db.PutSetting(ctx, s.pool, "retention", map[string]any{"events_days": 30, "metadata_days": 365, "bodies_days": 30}, "seed")
	}
	db.Notify(ctx, s.pool, db.ChanPolicy, "*")
	return nil
}

func imagesChanged(a, b policy.Catalog) bool {
	if len(a.Images) != len(b.Images) {
		return true
	}
	for k, v := range b.Images {
		if a.Images[k] != v {
			return true
		}
	}
	return false
}

// csrSANs validates a device CSR (signature, CN == device ID, DNS or URI SANs
// only) and returns its SANs.
func csrSANs(csrPEM, deviceID string) ([]string, error) {
	blk, _ := pem.Decode([]byte(csrPEM))
	if blk == nil {
		return nil, fmt.Errorf("device CSR is not PEM")
	}
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("device CSR: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("device CSR signature invalid")
	}
	if csr.Subject.CommonName != deviceID {
		return nil, fmt.Errorf("device CSR common name must be the device ID %q", deviceID)
	}
	if len(csr.IPAddresses) > 0 || len(csr.EmailAddresses) > 0 {
		return nil, fmt.Errorf("device CSR may only carry DNS or URI names")
	}
	sans := append([]string{}, csr.DNSNames...)
	for _, u := range csr.URIs {
		sans = append(sans, u.String())
	}
	if len(sans) == 0 {
		return nil, fmt.Errorf("device CSR needs at least one SAN")
	}
	return sans, nil
}

// imageFiles lists where the image publish step may drop images.json.
func imageFiles(sf seedFile) []string {
	var out []string
	if sf.Images != "" {
		out = append(out, sf.Images)
	}
	if d := os.Getenv("RELEASES_DIR"); d != "" {
		out = append(out, filepath.Join(d, "images.json"))
	}
	return out
}

// watchImages re-runs the seed when images.json appears or changes, or on SIGHUP.
func (s *Server) watchImages(ctx context.Context, seedPath string) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	stamp := func() string {
		var sf seedFile
		if b, err := os.ReadFile(seedPath); err == nil {
			json.Unmarshal(b, &sf)
		}
		var sb strings.Builder
		for _, p := range imageFiles(sf) {
			if st, err := os.Stat(p); err == nil {
				fmt.Fprintf(&sb, "%s:%d:%d;", p, st.ModTime().UnixNano(), st.Size())
			}
		}
		return sb.String()
	}
	last := stamp()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-hup:
			log.Printf("SIGHUP: re-running seed")
		case <-tick.C:
			if cur := stamp(); cur == last {
				continue
			} else {
				last = cur
			}
		case <-ctx.Done():
			return
		}
		if err := s.seed(ctx, seedPath); err != nil {
			log.Printf("seed: %v", err)
		}
	}
}
