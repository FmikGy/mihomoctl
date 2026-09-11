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
	"mihomoctl/internal/i18n"
)

func main() {
	preferences := app.DefaultPreferences()
	language, preferenceErr := preferences.LoadLanguage()
	explicitLanguage, explicit := i18n.FromArgs(os.Args[1:])
	if explicit {
		language = explicitLanguage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx = i18n.WithLanguage(ctx, language)
	ctx = i18n.WithPreferences(ctx, preferences)
	if preferenceErr != nil && !explicit {
		fmt.Fprintf(os.Stderr, "mihomoctl: %s\n", i18n.T(language, "读取语言偏好失败，已使用中文: %v", preferenceErr))
	}
	backend, err := app.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mihomoctl: %s\n", i18n.T(language, "初始化失败: %v", err))
		stop()
		os.Exit(startupExitCode(err))
	}
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
