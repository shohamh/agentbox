//go:build windows

package main

import (
	"log"
	"os"

	"golang.org/x/sys/windows/svc"
)

type serviceHandler struct{ run func() }

func (h *serviceHandler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	go h.run()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			if onStop != nil {
				onStop()
			}
			return false, 0
		}
	}
	return false, 0
}

// runAsService reports whether the process was started by the service manager.
func runAsService(run func()) bool {
	is, err := svc.IsWindowsService()
	if err != nil || !is {
		return false
	}
	if err := svc.Run("agentbox-host", &serviceHandler{run: run}); err != nil {
		log.Printf("service: %v", err)
		os.Exit(1)
	}
	return true
}
