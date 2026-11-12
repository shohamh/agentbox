package tpm

import (
	"os"
	"syscall"
)

// openBlocking opens a TPM character device in blocking mode.
//
// os.OpenFile registers a pollable character device such as /dev/tpmrm0 with
// Go's poller, which puts the descriptor in O_NONBLOCK mode. The kernel TPM
// driver then queues each command asynchronously, and a read issued before the
// command finishes returns 0 bytes, which Go reports as io.EOF. Fast commands
// (PCR_Read) usually win that race; slow ones (CreatePrimary for the
// attestation key on the Hyper-V virtual TPM) lose it. os.NewFile on a blocking
// descriptor keeps it blocking, so each write runs the command to completion.
func openBlocking(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
