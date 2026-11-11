//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// qemuDriver launches the appliance with QEMU/KVM as a transient systemd unit
// under the agentbox-vm user: OVMF with the organization's Secure Boot vars,
// swtpm as a TPM 2.0 (tpm-crb), the signed base image read-only with
// snapshot=on, a per-user qcow2 data disk, vhost-vsock for control and passt
// for the restricted uplink. Not exercised in the container test bed.
type qemuDriver struct {
	cfg Config
	cid int
}

func newQEMU(cfg Config) (Driver, error) {
	cid := cfg.VsockCID
	if cid == 0 {
		cid = 1000 + os.Getuid()%10000
	}
	return &qemuDriver{cfg: cfg, cid: cid}, nil
}

func (q *qemuDriver) Name() string        { return "qemu" }
func (q *qemuDriver) ChannelAddr() string { return "vsock://" + strconv.Itoa(q.cid) + ":7000" }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func (q *qemuDriver) Checks() []Check {
	var cs []Check
	cs = append(cs, Check{Name: "KVM", OK: exists("/dev/kvm"), Fix: "enable virtualization in firmware and load kvm_intel or kvm_amd"})
	cs = append(cs, Check{Name: "TPM 2.0", OK: exists("/dev/tpmrm0"), Fix: "enable the TPM in firmware"})
	cs = append(cs, Check{Name: "vhost-vsock", OK: exists("/dev/vhost-vsock"), Fix: "modprobe vhost_vsock"})
	for _, b := range []string{"qemu-system-x86_64", "swtpm", "passt"} {
		_, err := exec.LookPath(b)
		cs = append(cs, Check{Name: b, OK: err == nil, Fix: "reinstall the agentbox package (it depends on " + b + ")"})
	}
	cmdline, _ := os.ReadFile("/proc/cmdline")
	strict := strings.Contains(string(cmdline), "lockdown=integrity")
	cs = append(cs, Check{Name: "strict mode (level B)", OK: true, Info: map[bool]string{true: "on", false: "off (level C)"}[strict]})
	return cs
}

func (q *qemuDriver) Start(ctx context.Context) error {
	st := q.cfg.StateDir
	tpmDir := filepath.Join(st, "vtpm")
	os.MkdirAll(tpmDir, 0o700)
	if err := exec.CommandContext(ctx, "systemd-run", "--unit=agentbox-swtpm", "--property=User=agentbox-vm",
		"swtpm", "socket", "--tpm2", "--tpmstate", "dir="+tpmDir, "--ctrl", "type=unixio,path="+filepath.Join(st, "swtpm.sock"),
		"--flags", "startup-clear").Run(); err != nil {
		return fmt.Errorf("swtpm: %w", err)
	}
	args := []string{
		"--unit=agentbox-appliance", "--property=User=agentbox-vm", "--property=ProtectSystem=strict",
		"--property=NoNewPrivileges=yes", "--property=ReadWritePaths=" + st,
		"qemu-system-x86_64", "-machine", "q35,smm=on,accel=kvm", "-cpu", "host", "-smp", "4", "-m", "8192", "-nodefaults", "-nographic",
		"-global", "driver=cfi.pflash01,property=secure,value=on",
		"-drive", "if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.secboot.fd",
		"-drive", "if=pflash,format=raw,unit=1,file=" + filepath.Join(st, "OVMF_VARS.fd"),
		"-chardev", "socket,id=chrtpm,path=" + filepath.Join(st, "swtpm.sock"),
		"-tpmdev", "emulator,id=tpm0,chardev=chrtpm", "-device", "tpm-crb,tpmdev=tpm0",
		"-drive", "if=virtio,format=raw,readonly=on,snapshot=on,file=" + q.cfg.BaseImage,
		"-drive", "if=virtio,format=qcow2,file=" + filepath.Join(st, "data.qcow2"),
		"-device", "vhost-vsock-pci,guest-cid=" + strconv.Itoa(q.cid),
		"-netdev", "stream,id=net0,server=off,addr.type=unix,addr.path=" + filepath.Join(st, "passt.sock"),
		"-device", "virtio-net-pci,netdev=net0",
		"-qmp", "unix:" + filepath.Join(st, "qmp.sock") + ",server=on,wait=off",
	}
	return exec.CommandContext(ctx, "systemd-run", args...).Run()
}

func (q *qemuDriver) Stop(ctx context.Context) error {
	exec.CommandContext(ctx, "systemctl", "stop", "agentbox-appliance").Run()
	return exec.CommandContext(ctx, "systemctl", "stop", "agentbox-swtpm").Run()
}
