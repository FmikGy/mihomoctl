package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type ElevatedAction string

const (
	ElevatedInit            ElevatedAction = "init"
	ElevatedServiceStart    ElevatedAction = "service-start"
	ElevatedServiceStop     ElevatedAction = "service-stop"
	ElevatedServiceRestart  ElevatedAction = "service-restart"
	ElevatedServiceEnable   ElevatedAction = "service-enable"
	ElevatedServiceDisable  ElevatedAction = "service-disable"
	ElevatedTUNEnable       ElevatedAction = "tun-enable"
	ElevatedTUNDisable      ElevatedAction = "tun-disable"
	ElevatedScheduleEnable  ElevatedAction = "schedule-enable"
	ElevatedScheduleDisable ElevatedAction = "schedule-disable"
)

var elevatedArguments = map[ElevatedAction][]string{
	ElevatedInit:            {"init"},
	ElevatedServiceStart:    {"service", "start"},
	ElevatedServiceStop:     {"service", "stop"},
	ElevatedServiceRestart:  {"service", "restart"},
	ElevatedServiceEnable:   {"service", "enable"},
	ElevatedServiceDisable:  {"service", "disable"},
	ElevatedTUNEnable:       {"tun", "on"},
	ElevatedTUNDisable:      {"tun", "off"},
	ElevatedScheduleEnable:  {"schedule", "enable"},
	ElevatedScheduleDisable: {"schedule", "disable"},
}

// SudoReexecutor re-runs only a small allowlist of fixed CLI actions. No user
// text is accepted as a command or argument and no shell is involved.
type SudoReexecutor struct {
	Runner     CommandRunner
	Executable string
	SudoPath   string
	EUID       func() int
}

func NewSudoReexecutor(runner CommandRunner, executable string) (*SudoReexecutor, error) {
	if runner == nil {
		return nil, fmt.Errorf("sudo command runner is required")
	}
	if executable == "" || !filepath.IsAbs(executable) {
		return nil, fmt.Errorf("executable path must be absolute")
	}
	return &SudoReexecutor{
		Runner:     runner,
		Executable: executable,
		SudoPath:   "sudo",
		EUID:       os.Geteuid,
	}, nil
}

func (r *SudoReexecutor) NeedsElevation() bool {
	return r != nil && r.EUID != nil && r.EUID() != 0
}

func (r *SudoReexecutor) Run(ctx context.Context, action ElevatedAction) (CommandResult, error) {
	if r == nil || r.Runner == nil {
		return CommandResult{}, fmt.Errorf("sudo command runner is required")
	}
	if r.Executable == "" || !filepath.IsAbs(r.Executable) {
		return CommandResult{}, fmt.Errorf("executable path must be absolute")
	}
	args, ok := elevatedArguments[action]
	if !ok {
		return CommandResult{}, fmt.Errorf("unsupported privileged action %q", action)
	}
	args = append([]string(nil), args...)
	if !r.NeedsElevation() {
		return r.Runner.Run(ctx, r.Executable, args...)
	}
	sudo := r.SudoPath
	if sudo == "" {
		sudo = "sudo"
	}
	sudoArgs := append([]string{"--", r.Executable}, args...)
	return r.Runner.Run(ctx, sudo, sudoArgs...)
}
