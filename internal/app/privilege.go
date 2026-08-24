package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type PrivilegeError struct {
	Cause error
}

func (e *PrivilegeError) Error() string { return "操作需要管理员权限" }
func (e *PrivilegeError) Unwrap() error { return e.Cause }
func (e *PrivilegeError) ExitCode() int { return 3 }

type elevatedCommandError struct {
	code    int
	message string
}

func (e *elevatedCommandError) Error() string { return e.message }
func (e *elevatedCommandError) ExitCode() int { return e.code }

// runElevated re-executes only argv constructed by App methods. No shell is
// involved. Sensitive profile source text is carried on stdin, never argv.
func (a *App) runElevated(ctx context.Context, args []string, input string) error {
	if a.isRoot() {
		return errors.New("内部错误：root 进程不应再次提权")
	}
	executable, err := executablePath(a.executable)
	if err != nil {
		return &PrivilegeError{Cause: err}
	}
	commandArgs := append([]string{"--", executable}, args...)
	cmd := exec.CommandContext(ctx, "sudo", commandArgs...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	} else {
		cmd.Stdin = os.Stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := sanitizeElevatedError(stderr.String())
			if strings.HasPrefix(strings.ToLower(message), "sudo:") {
				return &PrivilegeError{Cause: fmt.Errorf("%s", message)}
			}
			if message == "" {
				message = "提权后的 mihomoctl 操作失败"
			}
			message = strings.TrimPrefix(message, "mihomoctl: ")
			code := exitErr.ExitCode()
			if code < 1 || code > 4 {
				code = 1
			}
			return &elevatedCommandError{code: code, message: message}
		}
		return &PrivilegeError{Cause: err}
	}
	return nil
}

func sanitizeElevatedError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 240 {
		last = last[:240]
	}
	return last
}

func stdinString(reader io.Reader, limit int64) (string, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(content)) > limit {
		return "", errors.New("输入超过大小限制")
	}
	return string(content), nil
}
