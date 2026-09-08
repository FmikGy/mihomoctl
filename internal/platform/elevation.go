package platform

import (
	"context"
	"io"
)

type elevationMode uint8

const (
	elevationDefault elevationMode = iota
	elevationInteractive
	elevationNonInteractive
)

type elevationModeContextKey struct{}
type elevationInputContextKey struct{}

// WithInteractiveElevation marks a call as safe to prompt on its attached
// terminal. TUI callers must only use it after releasing terminal ownership.
func WithInteractiveElevation(ctx context.Context) context.Context {
	return context.WithValue(nonNilContext(ctx), elevationModeContextKey{}, elevationInteractive)
}

// WithNonInteractiveElevation prevents sudo from prompting. It is intended for
// background attempts made while another component still owns terminal input.
func WithNonInteractiveElevation(ctx context.Context) context.Context {
	return context.WithValue(nonNilContext(ctx), elevationModeContextKey{}, elevationNonInteractive)
}

func InteractiveElevation(ctx context.Context) bool {
	return elevationModeFromContext(ctx) == elevationInteractive
}

func NonInteractiveElevation(ctx context.Context) bool {
	return elevationModeFromContext(ctx) == elevationNonInteractive
}

// WithElevationInput makes Bubble Tea's input available after it releases the
// terminal. Password handling remains entirely under sudo's control.
func WithElevationInput(ctx context.Context, stdin io.Reader) context.Context {
	return context.WithValue(nonNilContext(ctx), elevationInputContextKey{}, stdin)
}

func ElevationInput(ctx context.Context) (io.Reader, bool) {
	if ctx == nil {
		return nil, false
	}
	stdin, ok := ctx.Value(elevationInputContextKey{}).(io.Reader)
	return stdin, ok
}

func elevationModeFromContext(ctx context.Context) elevationMode {
	if ctx == nil {
		return elevationDefault
	}
	mode, _ := ctx.Value(elevationModeContextKey{}).(elevationMode)
	return mode
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
