//go:build windows

package main

import (
	"context"
	"log/slog"

	"golang.org/x/sys/windows/svc"
)

// windowsServiceName is the SCM name registered by install-windows.ps1.
// It is referenced here so a single rename keeps installer and binary
// in sync.
const windowsServiceName = "autodeploy"

// runPlatform on Windows checks whether the Service Control Manager
// started us. If so, it hands control to the SCM dispatcher; if not,
// the binary behaves like any console process so an operator can run
// it from PowerShell during initial setup or for troubleshooting.
func runPlatform(logger *slog.Logger) error {
	inService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !inService {
		return runConsole(logger)
	}
	return svc.Run(windowsServiceName, &winService{logger: logger})
}

// winService bridges the Windows SCM control protocol to the
// context-cancellation pattern the rest of the server already uses.
type winService struct {
	logger *slog.Logger
}

func (s *winService) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (svcSpecificEc bool, exitCode uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- run(ctx, s.logger) }()

	status <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
				s.logger.Warn("service.unexpected_control", slog.Any("cmd", req.Cmd))
			}
		case err := <-done:
			if err != nil {
				s.logger.Error("server.fatal", slog.String("error", err.Error()))
				status <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}
