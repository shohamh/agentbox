//go:build !windows

package main

import "fmt"

func newHyperV(Config) (Driver, error) { return nil, fmt.Errorf("the hyperv driver runs on Windows only") }
