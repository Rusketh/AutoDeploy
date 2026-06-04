//go:build windows

package main

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName    = "AutoDeployAgent"
	serviceDisplay = "AutoDeploy Agent"
	serviceDesc    = "AutoDeploy resident agent: polls the server for this machine's desired state and applies it."
)

// isWindowsService reports whether the process was launched by the Service
// Control Manager (vs. an interactive console run).
func isWindowsService() bool {
	in, err := svc.IsWindowsService()
	return err == nil && in
}

// agentService adapts our run loop to the svc.Handler interface.
type agentService struct {
	run func(ctx context.Context)
}

func (s *agentService) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	// Guarantee the run loop's context is cancelled on every return path
	// (the explicit cancel() on Stop below still fires first, so the loop
	// is signalled before we wait on done; a second cancel is a no-op).
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.run(ctx)
		close(done)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				cancel()
				status <- svc.Status{State: svc.StopPending}
				<-done
				return false, 0
			}
		case <-done:
			// Run loop returned on its own (shouldn't normally happen).
			return false, 0
		}
	}
}

// runService hands control to the SCM, running fn until the service is
// stopped (which cancels fn's context).
func runService(fn func(ctx context.Context)) error {
	return svc.Run(serviceName, &agentService{run: fn})
}

// installService registers this executable as an auto-start service and
// starts it. Idempotent: an existing service is removed and recreated so
// re-running the deploy reprovisions cleanly.
func installService() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	if existing, err := m.OpenService(serviceName); err == nil {
		_, _ = existing.Control(svc.Stop)
		_ = existing.Delete()
		existing.Close()
	}

	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: serviceDisplay,
		Description: serviceDesc,
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()
	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

// uninstallService stops and deletes the service.
func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service not installed: %w", err)
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	return s.Delete()
}
