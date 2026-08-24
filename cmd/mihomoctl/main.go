package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"mihomoctl/internal/app"
	"mihomoctl/internal/cli"
)

func main() {
	backend, err := app.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mihomoctl: 初始化失败: %v\n", err)
		os.Exit(startupExitCode(err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, backend, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func startupExitCode(err error) int {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		if code := coded.ExitCode(); code >= cli.ExitFailure && code <= cli.ExitUnavailable {
			return code
		}
	}
	if errors.Is(err, os.ErrPermission) {
		return cli.ExitPermission
	}
	return cli.ExitFailure
}
