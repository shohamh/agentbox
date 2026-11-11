//go:build !windows

package main

func runAsService(func()) bool { return false }
