#!/bin/sh
# Boots /vm/disk.vhdx under QEMU with UEFI Secure Boot (Microsoft keys), a
# software TPM behind tpm-crb, and a second disk for /data. No KVM here, so
# QEMU uses TCG (slow); /vm/serial.log holds the console.
set -eu
mkdir -p /tpm /vm
CODE=/usr/share/OVMF/OVMF_CODE_4M.secboot.fd; VARS_SRC=/usr/share/OVMF/OVMF_VARS_4M.ms.fd
[ -e "$CODE" ] || { echo "OVMF secure boot firmware not found"; ls /usr/share/OVMF; exit 1; }
cp "$VARS_SRC" /vm/vars.fd
[ -e /vm/data.raw ] || truncate -s 2G /vm/data.raw
swtpm_setup --tpm2 --tpmstate /tpm --createek --overwrite --config /etc/swtpm_setup.conf >/dev/null 2>&1 || true
swtpm socket --tpm2 --tpmstate dir=/tpm --ctrl type=unixio,path=/tpm/ctrl.sock --flags not-need-init,startup-clear -d
sleep 1
ACCEL=tcg; CPU=max
if [ -w /dev/kvm ]; then ACCEL=kvm; CPU=host; fi
echo "qemu accel=$ACCEL"
exec qemu-system-x86_64 -machine q35,smm=on,accel=$ACCEL -cpu $CPU -smp 2 -m 2048 -nographic -no-reboot \
  -global driver=cfi.pflash01,property=secure,value=on \
  -drive if=pflash,format=raw,unit=0,file=$CODE,readonly=on \
  -drive if=pflash,format=raw,unit=1,file=/vm/vars.fd \
  -chardev socket,id=chrtpm,path=/tpm/ctrl.sock -tpmdev emulator,id=tpm0,chardev=chrtpm -device tpm-crb,tpmdev=tpm0 \
  -drive file=/vm/disk.vhdx,format=vhdx,if=virtio \
  -drive file=/vm/data.raw,format=raw,if=virtio \
  -netdev user,id=n0,hostfwd=tcp::7000-:7000 -device virtio-net-pci,netdev=n0 \
  -serial file:/vm/serial.log -monitor none
