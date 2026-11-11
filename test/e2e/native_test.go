//go:build e2e

package e2e

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Native-PC variants of the steps that run shell commands on the PC itself.
// They apply when AGENTBOX_EXE is set and the PC under test is pc1.

func isNative(svc string) bool { return os.Getenv("AGENTBOX_EXE") != "" && svc == pc1 }

// pcSSH runs a command in a sandbox through the generated Remote-SSH entry.
func pcSSH(t testing.TB, pc, sandbox, command string) Result {
	t.Helper()
	if isNative(pc) {
		home, _ := os.UserHomeDir()
		return run(60*time.Second, "", "ssh", "-F", filepath.Join(home, ".ssh", "agentbox_config"), "agentbox-"+sandbox, command)
	}
	return sh(t, pc, `ssh -F "$HOME/.ssh/agentbox_config" agentbox-`+sandbox+` '`+command+`'`)
}

var skRe = regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`)

// nativeScan searches this PC's Agentbox files for any model credential: the
// install, state and key directories and the generated SSH config.
func nativeScan(t *testing.T, master string) string {
	home, _ := os.UserHomeDir()
	roots := []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "Agentbox"), filepath.Join(home, ".agentbox"), filepath.Join(home, ".ssh", "agentbox_config")}
	sk, m := 0, 0
	for _, root := range roots {
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			// Executables embed SSH key-type names such as sk-ssh-ed25519@openssh.com.
			if err != nil || info.IsDir() || info.Size() > 12<<20 || strings.EqualFold(filepath.Ext(p), ".exe") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			sk += len(skRe.FindAll(b, -1))
			if master != "" && strings.Contains(string(b), master) {
				m++
			}
			return nil
		})
	}
	return fmt.Sprintf("SK_HITS=%d\nMASTER_HITS=%d\n", sk, m)
}

// nativeHTTP sends a raw request (with printf-style backslash-r backslash-n
// escapes) over TCP and parses the response, so it returns as soon as the
// response is complete instead of waiting for the connection to close.
func nativeHTTP(host, port, req string) string {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		return err.Error()
	}
	defer c.Close()
	req = strings.NewReplacer(`\r`, "\r", `\n`, "\n", "%%", "%").Replace(req)
	c.Write([]byte(req))
	c.SetReadDeadline(time.Now().Add(25 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return fmt.Sprintf("HTTP/%d.%d %d\n%s", resp.ProtoMajor, resp.ProtoMinor, resp.StatusCode, body)
}

var (
	previewMu    sync.Mutex
	previewProcs []*exec.Cmd
)

// startPreview runs `agentbox preview` in the background on the PC.
func startPreview(t *testing.T, s sbInfo, port, listen string) Result {
	if isNative(s.PC) {
		cmd := exec.Command(os.Getenv("AGENTBOX_EXE"), "preview", s.Name, port, "--listen", listen)
		if err := cmd.Start(); err != nil {
			return Result{Out: err.Error(), Code: -1}
		}
		previewMu.Lock()
		previewProcs = append(previewProcs, cmd)
		previewMu.Unlock()
		return Result{}
	}
	return run(30*time.Second, "", "docker", "exec", "-d", cid(t, s.PC), "agentbox", "preview", s.Name, port, "--listen", listen)
}

func stopPreviews(t *testing.T, pc string) {
	if !isNative(pc) {
		sh(t, pc, `for p in /proc/[0-9]*; do tr '\0' ' ' < $p/cmdline 2>/dev/null | grep -q 'agentbox preview' && kill ${p#/proc/} 2>/dev/null; done; true`)
		return
	}
	previewMu.Lock()
	defer previewMu.Unlock()
	for _, c := range previewProcs {
		c.Process.Kill()
	}
	previewProcs = nil
}
