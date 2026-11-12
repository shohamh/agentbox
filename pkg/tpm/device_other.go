//go:build !linux

package tpm

import "os"

// openBlocking opens a TPM device. Only the Linux appliance talks to a TPM
// device; elsewhere this is a plain open.
func openBlocking(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, 0)
}
