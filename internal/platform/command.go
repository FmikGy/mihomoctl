package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// CommandResult contains captured process output. Callers should avoid placing
// Stdout or Stderr in user-facing errors because either stream may contain a
// subscription credential.
type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// CommandRunner makes every host command replaceable in tests. Implementations
// must execute name directly and must not pass it through a shell.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (CommandResult, error)
}

// ExecRunner executes commands with os/exec and captures both output streams.
type ExecRunner struct {
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.Dir
	if r.Env != nil {
		cmd.Env = append(os.Environ(), r.Env...)
	}
	cmd.Stdin = r.Stdin

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if r.Stdout != nil {
		cmd.Stdout = io.MultiWriter(&stdout, r.Stdout)
	}
	if r.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&stderr, r.Stderr)
	}
	err := cmd.Run()
	result := CommandResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: 0,
	}
	if err == nil {
		return result, nil
	}

	result.ExitCode = -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result, &CommandError{Name: name, ExitCode: result.ExitCode, Err: err}
}

// CommandError deliberately omits command arguments and output to keep secrets
// out of logs. The original error remains available through errors.Is/As.
type CommandError struct {
	Name     string
	ExitCode int
	Err      error
}

func (e *CommandError) Error() string {
	if e.ExitCode >= 0 {
		return fmt.Sprintf("%s exited with status %d", e.Name, e.ExitCode)
	}
	return fmt.Sprintf("could not execute %s", e.Name)
}

func (e *CommandError) Unwrap() error { return e.Err }
