package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agentbox/pkg/tpm"
)

func sh(timeout time.Duration, script string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "sh", "-c", script).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// diagnostics answers `agentbox doctor`: boot evidence and state of the
// appliance, with no secrets (no tokens, keys or bundle contents).
func (m *Manager) diagnostics(w http.ResponseWriter, _ *http.Request) {
	d := map[string]any{"release": m.rel}
	if conn, _, _, _, ok := m.tpmState(); ok {
		if pcr, err := tpm.ReadPCR(conn); err == nil {
			d["pcr11"] = hex.EncodeToString(pcr)
		} else {
			d["pcr11_error"] = err.Error()
		}
	} else {
		d["pcr11_error"] = "TPM not ready: " + faultList()["tpm"]
	}
	d["faults"] = faultList()
	if b, err := os.ReadFile("/run/agentbox/measure.error"); err == nil {
		d["measure_error"] = strings.TrimSpace(string(b))
	}
	d["tpm_devices"] = sh(3*time.Second, "ls -la /dev/tpm* 2>&1; dmesg 2>/dev/null | grep -i -E 'tpm|hv_sock|vsock' | tail -12")
	d["modules"] = sh(3*time.Second, "lsmod | grep -E 'hv_|vsock|tpm' | cut -d' ' -f1 | paste -sd ' '")
	if b, err := os.ReadFile("/proc/version"); err == nil {
		d["kernel"] = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/cmdline"); err == nil {
		d["cmdline"] = strings.TrimSpace(string(b))
	}
	d["secure_boot"] = secureBoot()
	d["tpm_path"] = m.cfg.TPMSocket
	d["addresses"] = sh(3*time.Second, "ip -o -4 addr show scope global | awk '{print $2, $4}'")
	d["default_route"] = sh(3*time.Second, "ip route show default")
	d["agentbox_hosts"] = sh(3*time.Second, "grep agentbox.corp /etc/hosts")
	if b, err := os.ReadFile(filepath.Join(m.cfg.RunDir, "eventlog.json")); err == nil {
		var ev any
		if json.Unmarshal(b, &ev) == nil {
			d["event_log"] = ev
		}
	}
	d["data_disk"] = sh(3*time.Second, "df -h /data | tail -1")
	d["time"] = time.Now().UTC().Format(time.RFC3339)
	d["time_source"] = sh(3*time.Second, "chronyc tracking 2>&1 | head -3")
	d["services"] = sh(5*time.Second, "systemctl is-active agentbox-appliance chrony systemd-networkd 2>&1 | paste -sd ' '")
	d["journal"] = sh(5*time.Second, "journalctl -u agentbox-appliance -u agentbox-site -u agentbox-data -n 80 --no-pager -o short 2>&1")
	d["nft_agentbox_table"] = sh(3*time.Second, "nft list table inet agentbox 2>&1 | head -3")
	jsonOut(w, 200, d)
}

// secureBoot reads the UEFI SecureBoot variable (last byte 1 = enabled).
func secureBoot() string {
	b, err := os.ReadFile("/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c")
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	if len(b) > 0 && b[len(b)-1] == 1 {
		return "on"
	}
	return "off"
}
