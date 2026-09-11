package tui

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"

	"mihomoctl/internal/i18n"
	"mihomoctl/internal/platform"
)

type elevationAwareBackend interface {
	NeedsElevation() bool
}

type authorizationRequiredError interface {
	AuthorizationRequired() bool
}

type operationWarningError interface {
	Warning() bool
}

func isOperationWarning(err error) bool {
	if err == nil {
		return false
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		children := many.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !isOperationWarning(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return isOperationWarning(wrapped.Unwrap())
	}
	target, ok := err.(operationWarningError)
	return ok && target.Warning()
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
	return m.beginOperationAttemptWithCore(action, message, "", "", m.needsElevation(), true, false, fn)
}

func (m *Model) beginPrivilegedCoreOperation(action, message string, fn func(context.Context) error) tea.Cmd {
	return m.beginOperationAttemptWithCore(action, message, "", "", m.needsElevation(), true, true, fn)
}

func (m *Model) beginPrivilegedMetadataOperation(action, message string, fn func(context.Context) error) tea.Cmd {
	return m.beginOperationAttemptWithCore(action, message, "", "", m.needsElevation(), false, false, fn)
}

func (m *Model) beginPrivilegedConfigOperation(action, message, key, value string) tea.Cmd {
	return m.beginOperationAttemptWithCore(action, message, key, value, m.needsElevation(), true, true, func(ctx context.Context) error {
		return m.backend.SetConfig(ctx, key, value)
	})
}

func (m *Model) beginOperationAttempt(action, message, configKey, configValue string, privileged, runtimeMutation bool, fn func(context.Context) error) tea.Cmd {
	return m.beginOperationAttemptWithCore(action, message, configKey, configValue, privileged, runtimeMutation, false, fn)
}

func (m *Model) beginOperationAttemptWithCore(action, message, configKey, configValue string, privileged, runtimeMutation, coreMutation bool, fn func(context.Context) error) tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	m.privilegedOperation = privileged
	m.authorizing = false
	m.runtimeMutation = runtimeMutation
	m.coreMutation = coreMutation
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
				coreMutation: coreMutation,
			}
		}
		defer ticket.finish()
		err := fn(ctx)
		return operationMsg{
			action: action, message: message, err: err, generation: generation,
			configKey: configKey, configValue: configValue,
			privileged: privileged, coreMutation: coreMutation, retry: fn,
		}
	}
}

func (m Model) retryPrivilegedOperation(msg operationMsg) tea.Cmd {
	if msg.retry == nil {
		return func() tea.Msg {
			msg.err = i18n.Errorf("无法重试管理员操作")
			msg.interactive = true
			return msg
		}
	}
	command := &interactiveOperationCommand{
		ctx: m.ctx, action: msg.action, language: m.language, run: msg.retry,
		ticket: reserveOperation(m.operations),
	}
	return execInteractiveOperation(command, func(err error) tea.Msg {
		if err != nil && !command.started {
			err = i18n.Errorf("无法打开 sudo 授权终端: %w", err)
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
	ctx      context.Context
	action   string
	language i18n.Language
	run      func(context.Context) error
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	started  bool
	ticket   operationTicket
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
		_, _ = fmt.Fprintln(writer, authorizationPromptForLanguage(c.language, c.action))
	}
	ctx := platform.WithInteractiveElevation(c.ctx)
	ctx = platform.WithElevationInput(ctx, c.stdin)
	return c.run(ctx)
}

func (c *interactiveOperationCommand) SetStdin(stdin io.Reader)   { c.stdin = stdin }
func (c *interactiveOperationCommand) SetStdout(stdout io.Writer) { c.stdout = stdout }
func (c *interactiveOperationCommand) SetStderr(stderr io.Writer) { c.stderr = stderr }

func authorizationPrompt(action string) string {
	return authorizationPromptForLanguage(i18n.Chinese, action)
}

func authorizationPromptForLanguage(language i18n.Language, action string) string {
	action = truncate(i18n.T(language, action), 20)
	if action == "" {
		action = i18n.T(language, "执行此操作")
	}
	return i18n.T(language, "mihomoctl：sudo 验证后自动继续：“%s”", action)
}
