package tui

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"

	"mihomoctl/internal/platform"
)

type elevationAwareBackend interface {
	NeedsElevation() bool
}

type authorizationRequiredError interface {
	AuthorizationRequired() bool
}

var execInteractiveOperation = tea.Exec

func (m Model) needsElevation() bool {
	backend, ok := m.backend.(elevationAwareBackend)
	return ok && backend.NeedsElevation()
}

func isAuthorizationRequired(err error) bool {
	if err == nil {
		return false
	}
	var target authorizationRequiredError
	return errors.As(err, &target) && target.AuthorizationRequired()
}

func (m *Model) beginPrivilegedOperation(action, message string, fn func(context.Context) error) tea.Cmd {
	return m.beginOperationAttempt(action, message, "", "", m.needsElevation(), true, fn)
}

func (m *Model) beginPrivilegedConfigOperation(action, message, key, value string) tea.Cmd {
	return m.beginOperationAttempt(action, message, key, value, m.needsElevation(), true, func(ctx context.Context) error {
		return m.backend.SetConfig(ctx, key, value)
	})
}

func (m *Model) beginOperationAttempt(action, message, configKey, configValue string, privileged, runtimeMutation bool, fn func(context.Context) error) tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	m.privilegedOperation = privileged
	m.authorizing = false
	m.runtimeMutation = runtimeMutation
	if runtimeMutation {
		m.suspendRuntimeObservers()
	}
	m.mutationGeneration++
	m.clearForegroundErrors()
	generation := m.mutationGeneration
	ctx := m.ctx
	ticket := reserveOperation(m.operations)
	if privileged {
		ctx = platform.WithNonInteractiveElevation(ctx)
	}
	return func() tea.Msg {
		if !ticket.start() {
			return operationMsg{
				action: action, message: message, err: context.Canceled, generation: generation,
				configKey: configKey, configValue: configValue, privileged: privileged,
			}
		}
		defer ticket.finish()
		err := fn(ctx)
		return operationMsg{
			action: action, message: message, err: err, generation: generation,
			configKey: configKey, configValue: configValue,
			privileged: privileged, retry: fn,
		}
	}
}

func (m Model) retryPrivilegedOperation(msg operationMsg) tea.Cmd {
	if msg.retry == nil {
		return func() tea.Msg {
			msg.err = errors.New("无法重试管理员操作")
			msg.interactive = true
			return msg
		}
	}
	command := &interactiveOperationCommand{
		ctx: m.ctx, action: msg.action, run: msg.retry,
		ticket: reserveOperation(m.operations),
	}
	return execInteractiveOperation(command, func(err error) tea.Msg {
		if err != nil && !command.started {
			err = fmt.Errorf("无法打开 sudo 授权终端: %w", err)
		}
		msg.err = err
		msg.interactive = true
		msg.retry = nil
		return msg
	})
}

// interactiveOperationCommand lets Bubble Tea restore the terminal before an
// operation asks sudo for a password. Bubble Tea's input is forwarded to the
// nested operation; sudo itself reads passwords from the controlling terminal.
type interactiveOperationCommand struct {
	ctx     context.Context
	action  string
	run     func(context.Context) error
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	started bool
	ticket  operationTicket
}

func (c *interactiveOperationCommand) Run() error {
	if !c.ticket.start() {
		return context.Canceled
	}
	defer c.ticket.finish()
	c.started = true
	writer := c.stdout
	if writer == nil {
		writer = c.stderr
	}
	if writer != nil {
		_, _ = fmt.Fprintln(writer, authorizationPrompt(c.action))
	}
	ctx := platform.WithInteractiveElevation(c.ctx)
	ctx = platform.WithElevationInput(ctx, c.stdin)
	return c.run(ctx)
}

func (c *interactiveOperationCommand) SetStdin(stdin io.Reader)   { c.stdin = stdin }
func (c *interactiveOperationCommand) SetStdout(stdout io.Writer) { c.stdout = stdout }
func (c *interactiveOperationCommand) SetStderr(stderr io.Writer) { c.stderr = stderr }

func authorizationPrompt(action string) string {
	action = truncate(action, 20)
	if action == "" {
		action = "执行此操作"
	}
	return fmt.Sprintf("mihomoctl：sudo 验证后自动继续：“%s”", action)
}
