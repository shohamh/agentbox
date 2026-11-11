// agentbox is the CLI: identical on Windows and Linux, talking to
// agentbox-host over a Unix socket or named pipe.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"agentbox/pkg/hostapi"
)

var cl *hostapi.Client

func die(err error) {
	fmt.Fprintln(os.Stderr, "agentbox:", err)
	os.Exit(1)
}

func home() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".agentbox")
}

const usage = `agentbox - sandboxed AI coding agents on your own PC

  enroll [--user U --password-stdin]   sign in and register this PC
  status                               state of every layer, with fixes
  new <repo> [--name N] [--image I] [--harness codex|opencode]
  list | stop <sb> | start <sb> | rm <sb> | recycle <sb>
  shell <sb> [-- command...]           terminal in a sandbox (ssh)
  ssh-proxy <sb>                       ProxyCommand for ssh and IDEs
  code <sb>                            open in VS Code Remote-SSH
  preview <sb> <port> [--listen :N]    forward a web preview to localhost
  allow <kind:value> --reason "..."    request a permission (egress:host, image:name, port:n, tier:n)
  perms                                list your permission requests
  key <sb>                             base URLs and the local token for a sandbox
  up | recycle-appliance | doctor
`

func main() {
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "--help" {
		fmt.Print(usage)
		return
	}
	cl = hostapi.New(hostapi.DefaultAddress())
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "enroll":
		err = enroll(args)
	case "status":
		err = status()
	case "new":
		err = newSandbox(args)
	case "list", "ls":
		err = list()
	case "stop", "start", "recycle":
		err = action(cmd, args)
	case "rm":
		err = remove(args)
	case "shell":
		err = shell(args)
	case "ssh-proxy":
		err = sshProxy(args)
	case "code":
		err = code(args)
	case "preview":
		err = preview(args)
	case "allow":
		err = allow(args)
	case "perms":
		err = perms()
	case "key":
		err = key(args)
	case "recycle-appliance":
		err = cl.Do("POST", "/v1/recycle", nil, nil)
	case "up":
		err = cl.Do("GET", "/v1/checks", nil, nil)
	case "doctor":
		err = doctor()
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		die(err)
	}
}

// ---------- keys ----------

func sshKey() (priv, pub string, err error) {
	dir := home()
	priv, pub = filepath.Join(dir, "id_ed25519"), filepath.Join(dir, "id_ed25519.pub")
	if _, e := os.Stat(priv); e == nil {
		return priv, pub, nil
	}
	os.MkdirAll(dir, 0o700)
	pk, sk, _ := ed25519.GenerateKey(rand.Reader)
	blk, err := ssh.MarshalPrivateKey(sk, "agentbox")
	if err != nil {
		return "", "", err
	}
	spk, _ := ssh.NewPublicKey(pk)
	if err := os.WriteFile(priv, pem.EncodeToMemory(blk), 0o600); err != nil {
		return "", "", err
	}
	return priv, pub, os.WriteFile(pub, ssh.MarshalAuthorizedKey(spk), 0o644)
}

// ---------- commands ----------

func enroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	user := fs.String("user", "", "username (test beds with password sign-in)")
	pw := fs.Bool("password-stdin", false, "read the password from stdin")
	fs.Parse(args)
	if _, _, err := sshKey(); err != nil {
		return err
	}
	var res map[string]any
	if *user != "" && *pw {
		b, _ := io.ReadAll(os.Stdin)
		if err := cl.Do("POST", "/v1/enroll", map[string]string{"username": *user, "password": strings.TrimSpace(string(b))}, &res); err != nil {
			return err
		}
	} else {
		var d struct {
			DeviceCode  string `json:"device_code"`
			UserCode    string `json:"user_code"`
			URI         string `json:"verification_uri"`
			URIComplete string `json:"verification_uri_complete"`
			Interval    int    `json:"interval"`
		}
		if err := cl.Do("POST", "/v1/enroll/device-start", nil, &d); err != nil {
			return err
		}
		fmt.Printf("Open %s and enter the code %s\n(or open %s)\nWaiting for you to sign in...\n", d.URI, d.UserCode, d.URIComplete)
		if err := cl.Do("POST", "/v1/enroll/device-finish", map[string]any{"device_code": d.DeviceCode, "interval": d.Interval}, &res); err != nil {
			return err
		}
	}
	fmt.Printf("Enrolled %v as %v. The appliance is attesting; run agentbox status.\n", res["device_id"], res["user"])
	return nil
}

func yn(b bool) string {
	if b {
		return "ok  "
	}
	return "FAIL"
}

func status() error {
	var s map[string]any
	if err := cl.Do("GET", "/v1/status", nil, &s); err != nil {
		return err
	}
	fmt.Printf("device      %v (%v driver)\n", s["device_id"], s["driver"])
	if cs, ok := s["checks"].([]any); ok {
		for _, c := range cs {
			m := c.(map[string]any)
			fmt.Printf("  [%s] %v %v\n", yn(m["ok"] == true), m["name"], orEmpty(m["info"]))
			if m["ok"] != true && m["fix"] != nil {
				fmt.Printf("         fix: %v\n", m["fix"])
			}
		}
	}
	fmt.Printf("enrolled    %v\n", s["enrolled"])
	a, _ := s["appliance"].(map[string]any)
	if a["reachable"] != true {
		fmt.Printf("appliance   UNREACHABLE: %v\n  fix: %v\n", a["error"], a["fix"])
		return nil
	}
	att, _ := a["attestation"].(map[string]any)
	fmt.Printf("appliance   release %v (%v)\n", mapGet(a, "release", "version"), mapGet(a, "release", "flavor"))
	fmt.Printf("identity    %v / %v\n", a["device_id"], a["user"])
	fmt.Printf("attestation %v %v\n", att["status"], orEmpty(att["reason"]))
	if b, ok := a["bundle"].(map[string]any); ok {
		fmt.Printf("policy      bundle v%v, tier %v, level %v, expires %v\n", b["version"], b["tier"], b["assurance_level"], b["expires_at"])
		fmt.Printf("model       %v\n", b["models"])
		fmt.Printf("egress      %v\n", b["egress"])
	} else {
		fmt.Println("policy      none yet")
	}
	if e := a["control_plane_error"]; e != nil && e != "" {
		fmt.Printf("control     UNREACHABLE: %v (sandboxes keep running; model calls fail until it recovers)\n", e)
	} else {
		fmt.Println("control     reachable")
	}
	fmt.Printf("sandboxes   %v running of %v\n", a["sandboxes_running"], a["sandboxes_total"])
	return nil
}

func orEmpty(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func mapGet(m map[string]any, k, k2 string) any {
	if mm, ok := m[k].(map[string]any); ok {
		return mm[k2]
	}
	return nil
}

type sandbox struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	Harness  string `json:"harness"`
	IP       string `json:"ip"`
	State    string `json:"state"`
	StopNote string `json:"stop_reason"`
	Error    string `json:"error"`
}

func newSandbox(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	name := fs.String("name", "", "sandbox name")
	image := fs.String("image", "", "sandbox image")
	harness := fs.String("harness", "", "codex or opencode")
	// allow the repo before or after flags
	var pos []string
	for len(args) > 0 {
		fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	repo := ""
	if len(pos) > 0 {
		repo = pos[0]
	}
	_, pub, err := sshKey()
	if err != nil {
		return err
	}
	pb, _ := os.ReadFile(pub)
	var s sandbox
	if err := cl.Do("POST", "/v1/sandboxes", map[string]string{"name": *name, "repo": repo, "image": *image, "harness": *harness, "ssh_pub": strings.TrimSpace(string(pb))}, &s); err != nil {
		return err
	}
	fmt.Printf("%s  %s  %s\n", s.Name, s.State, s.Image)
	return nil
}

func list() error {
	var l []sandbox
	if err := cl.Do("GET", "/v1/sandboxes", nil, &l); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tIMAGE\tSTATE\tNOTE")
	for _, s := range l {
		note := s.Error
		if s.StopNote != "" {
			note = s.StopNote
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.ID, s.Image, s.State, note)
	}
	return tw.Flush()
}

func action(cmd string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agentbox %s <sandbox>", cmd)
	}
	var s sandbox
	if err := cl.Do("POST", "/v1/sandboxes/"+args[0]+"/"+cmd, nil, &s); err != nil {
		return err
	}
	fmt.Printf("%s  %s %s\n", s.Name, s.State, s.StopNote)
	return nil
}

func remove(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agentbox rm <sandbox>")
	}
	return cl.Do("DELETE", "/v1/sandboxes/"+args[0], nil, nil)
}

// sshProxy is the ProxyCommand: stdin/stdout joined to the sandbox's sshd.
func sshProxy(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agentbox ssh-proxy <sandbox>")
	}
	c, err := cl.Tunnel("/v1/sandboxes/" + args[0] + "/tunnel?port=22")
	if err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(c, os.Stdin); done <- struct{}{} }()
	go func() { io.Copy(os.Stdout, c); done <- struct{}{} }()
	<-done
	return nil
}

func sshArgs(sb string) ([]string, error) {
	priv, _, err := sshKey()
	if err != nil {
		return nil, err
	}
	self, _ := os.Executable()
	null := "/dev/null"
	if runtime.GOOS == "windows" {
		null = "NUL"
	}
	return []string{"-i", priv, "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=" + null, "-o", "LogLevel=ERROR",
		"-o", fmt.Sprintf("ProxyCommand=\"%s\" ssh-proxy %s", self, sb), "root@" + sb}, nil
}

func shell(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agentbox shell <sandbox> [-- command...]")
	}
	a, err := sshArgs(args[0])
	if err != nil {
		return err
	}
	rest := args[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 && !term.IsTerminal(int(os.Stdin.Fd())) {
		a = append([]string{"-T"}, a...)
	}
	cmd := exec.Command("ssh", append(a, rest...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// code writes a managed SSH config include and launches VS Code Remote-SSH.
func code(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agentbox code <sandbox>")
	}
	priv, _, err := sshKey()
	if err != nil {
		return err
	}
	self, _ := os.Executable()
	h, _ := os.UserHomeDir()
	cfg := fmt.Sprintf("Host agentbox-%[1]s\n  HostName %[1]s\n  User root\n  IdentityFile %[2]s\n  IdentitiesOnly yes\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  ProxyCommand %[3]s ssh-proxy %[1]s\n\n", args[0], priv, self)
	p := filepath.Join(h, ".ssh", "agentbox_config")
	os.MkdirAll(filepath.Dir(p), 0o700)
	old, _ := os.ReadFile(p)
	if !strings.Contains(string(old), "Host agentbox-"+args[0]+"\n") {
		os.WriteFile(p, append(old, []byte(cfg)...), 0o600)
	}
	fmt.Printf("SSH entry agentbox-%s written to %s (add \"Include agentbox_config\" to ~/.ssh/config)\n", args[0], p)
	if path, err := exec.LookPath("code"); err == nil {
		return exec.Command(path, "--remote", "ssh-remote+agentbox-"+args[0], "/workspace").Run()
	}
	fmt.Println("VS Code (code) is not on PATH; open Remote-SSH and connect to agentbox-" + args[0])
	return nil
}

func preview(args []string) error {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:0", "local address")
	var pos []string
	for len(args) > 0 {
		fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: agentbox preview <sandbox> <port> [--listen addr]")
	}
	if _, err := strconv.Atoi(pos[1]); err != nil {
		return fmt.Errorf("port must be a number")
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	fmt.Printf("Preview of %s:%s at http://%s (Ctrl-C to stop)\n", pos[0], pos[1], l.Addr())
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			t, err := cl.Tunnel(fmt.Sprintf("/v1/sandboxes/%s/preview?port=%s", pos[0], pos[1]))
			if err != nil {
				c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
				c.Close()
				return
			}
			go func() { io.Copy(t, c); t.Close() }()
			io.Copy(c, t)
			c.Close()
		}()
	}
}

type permReq struct {
	ID             int64  `json:"id"`
	Permission     string `json:"permission"`
	Class          string `json:"class"`
	Status         string `json:"status"`
	DecidedBy      string `json:"decided_by"`
	DecisionReason string `json:"decision_reason"`
	ReviewStatus   string `json:"review_status"`
	Message        string `json:"message"`
}

func allow(args []string) error {
	fs := flag.NewFlagSet("allow", flag.ExitOnError)
	reason := fs.String("reason", "", "why you need it")
	wait := fs.Int("wait", 120, "seconds to wait for an approval decision (0 to not wait)")
	var pos []string
	for len(args) > 0 {
		fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: agentbox allow <kind:value> --reason \"...\"")
	}
	var r permReq
	if err := cl.Do("POST", "/v1/permissions", map[string]string{"permission": pos[0], "reason": *reason}, &r); err != nil {
		return err
	}
	fmt.Printf("%s: %s (%s) - %s\n", r.Permission, r.Status, r.Class, r.Message)
	if r.Status == "pending" && *wait > 0 {
		fmt.Println("Waiting for an approver...")
		deadline := time.Now().Add(time.Duration(*wait) * time.Second)
		for r.Status == "pending" && time.Now().Before(deadline) {
			if err := cl.Do("GET", fmt.Sprintf("/v1/permissions/%d?wait=20", r.ID), nil, &r); err != nil {
				return err
			}
		}
		switch r.Status {
		case "approved":
			fmt.Printf("Approved by %s. %s\n", r.DecidedBy, r.DecisionReason)
		case "denied":
			return fmt.Errorf("denied by %s: %s", r.DecidedBy, r.DecisionReason)
		default:
			fmt.Println("Still pending; check later with agentbox perms.")
		}
	} else if r.Status == "denied" {
		return fmt.Errorf("denied: %s", r.DecisionReason)
	}
	return nil
}

func perms() error {
	var l []permReq
	if err := cl.Do("GET", "/v1/permissions", nil, &l); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPERMISSION\tCLASS\tSTATUS\tBY\tREVIEW")
	for _, r := range l {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Permission, r.Class, r.Status, r.DecidedBy, r.ReviewStatus)
	}
	return tw.Flush()
}

func key(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agentbox key <sandbox>")
	}
	var k map[string]any
	if err := cl.Do("GET", "/v1/sandboxes/"+args[0]+"/key", nil, &k); err != nil {
		return err
	}
	fmt.Printf("OPENAI_BASE_URL=%v\nANTHROPIC_BASE_URL=%v\nAGENTBOX_TOKEN=%v\n# %v\n", k["openai_base_url"], k["anthropic_base_url"], k["token"], k["note"])
	return nil
}

func doctor() error {
	fmt.Println("agentbox doctor: redacted diagnostics")
	if err := status(); err != nil {
		fmt.Println("status error:", err)
	}
	return list()
}
