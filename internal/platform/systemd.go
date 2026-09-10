package platform

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"mihomoctl/internal/domain"
)

var unitNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:-]*\.(service|timer)$`)

// Systemd controls one fixed service unit. It never elevates privileges on its
// own; callers are responsible for enforcing privilege at the application boundary.
type Systemd struct {
	Runner CommandRunner
	Unit   string
}

type ServiceIdentity struct {
	User        string
	Group       string
	DynamicUser bool
}

func NewSystemd(runner CommandRunner, unit string) (*Systemd, error) {
	if runner == nil {
		return nil, fmt.Errorf("systemd command runner is required")
	}
	if !unitNamePattern.MatchString(unit) {
		return nil, fmt.Errorf("invalid systemd unit %q", unit)
	}
	return &Systemd{Runner: runner, Unit: unit}, nil
}

func (s *Systemd) Status(ctx context.Context) (domain.ServiceStatus, error) {
	if err := s.validate(); err != nil {
		return domain.ServiceStatus{}, err
	}
	result, err := s.Runner.Run(ctx, "systemctl", "show", "--no-pager",
		"--property=LoadState", "--property=ActiveState", "--property=SubState",
		"--property=UnitFileState", "--property=MainPID", "--", s.Unit)
	if err != nil {
		return domain.ServiceStatus{}, fmt.Errorf("query %s status: %w", s.Unit, err)
	}
	properties := parseProperties(string(result.Stdout))
	if properties["LoadState"] == "not-found" || properties["LoadState"] == "" {
		return domain.ServiceStatus{}, fmt.Errorf("systemd unit %s is not loaded", s.Unit)
	}

	pid, _ := strconv.Atoi(properties["MainPID"])
	state := properties["SubState"]
	if state == "" {
		state = properties["ActiveState"]
	}
	unitFileState := properties["UnitFileState"]
	return domain.ServiceStatus{
		Active:  properties["ActiveState"] == "active",
		Enabled: unitFileState == "enabled" || unitFileState == "enabled-runtime",
		State:   state,
		PID:     pid,
	}, nil
}

// Identity returns the account systemd uses to start the service. Empty User
// and Group values have systemd's usual root defaults.
func (s *Systemd) Identity(ctx context.Context) (ServiceIdentity, error) {
	if err := s.validate(); err != nil {
		return ServiceIdentity{}, err
	}
	result, err := s.Runner.Run(ctx, "systemctl", "show", "--no-pager",
		"--property=LoadState", "--property=User", "--property=Group",
		"--property=DynamicUser", "--", s.Unit)
	if err != nil {
		return ServiceIdentity{}, fmt.Errorf("query %s identity: %w", s.Unit, err)
	}
	properties := parseProperties(string(result.Stdout))
	if properties["LoadState"] == "not-found" || properties["LoadState"] == "" {
		return ServiceIdentity{}, fmt.Errorf("systemd unit %s is not loaded", s.Unit)
	}
	return ServiceIdentity{
		User: strings.TrimSpace(properties["User"]), Group: strings.TrimSpace(properties["Group"]),
		DynamicUser: strings.EqualFold(properties["DynamicUser"], "yes"),
	}, nil
}

func (s *Systemd) Start(ctx context.Context) error   { return s.action(ctx, "start") }
func (s *Systemd) Stop(ctx context.Context) error    { return s.action(ctx, "stop") }
func (s *Systemd) Enable(ctx context.Context) error  { return s.action(ctx, "enable") }
func (s *Systemd) Disable(ctx context.Context) error { return s.action(ctx, "disable") }
func (s *Systemd) EnableNow(ctx context.Context) error {
	return s.actionWithOptions(ctx, "enable", "--now")
}
func (s *Systemd) DisableNow(ctx context.Context) error {
	return s.actionWithOptions(ctx, "disable", "--now")
}

func (s *Systemd) Restart(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	if _, err := s.Runner.Run(ctx, "systemctl", "reset-failed", "--", s.Unit); err != nil {
		return fmt.Errorf("systemctl reset-failed %s: %w", s.Unit, err)
	}
	return s.action(ctx, "restart")
}

func (s *Systemd) action(ctx context.Context, action string) error {
	return s.actionWithOptions(ctx, action)
}

func (s *Systemd) actionWithOptions(ctx context.Context, action string, options ...string) error {
	if err := s.validate(); err != nil {
		return err
	}
	switch action {
	case "start", "stop", "restart", "enable", "disable":
	default:
		return fmt.Errorf("unsupported systemd action %q", action)
	}
	args := append([]string{action}, options...)
	args = append(args, "--", s.Unit)
	if _, err := s.Runner.Run(ctx, "systemctl", args...); err != nil {
		return fmt.Errorf("systemctl %s %s: %w", action, s.Unit, err)
	}
	return nil
}

func (s *Systemd) validate() error {
	if s == nil || s.Runner == nil {
		return fmt.Errorf("systemd command runner is required")
	}
	if !unitNamePattern.MatchString(s.Unit) {
		return fmt.Errorf("invalid systemd unit %q", s.Unit)
	}
	return nil
}

func parseProperties(output string) map[string]string {
	properties := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			properties[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return properties
}
