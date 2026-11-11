//go:build !linux

package main

import "fmt"

func newQEMU(Config) (Driver, error) { return nil, fmt.Errorf("the qemu driver runs on Linux only") }
