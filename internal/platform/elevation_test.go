package platform

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestElevationModesAreExplicitAndOverrideable(t *testing.T) {
	ctx := context.Background()
	if InteractiveElevation(ctx) || NonInteractiveElevation(ctx) {
		t.Fatal("default context unexpectedly selected an elevation mode")
	}
	ctx = WithNonInteractiveElevation(ctx)
	if !NonInteractiveElevation(ctx) || InteractiveElevation(ctx) {
		t.Fatal("non-interactive mode was not recorded")
	}
	ctx = WithInteractiveElevation(ctx)
	if !InteractiveElevation(ctx) || NonInteractiveElevation(ctx) {
		t.Fatal("interactive mode did not override the earlier mode")
	}
}

func TestElevationHelpersAcceptNilContextAndPreserveInput(t *testing.T) {
	var stdin bytes.Buffer
	ctx := WithElevationInput(nil, &stdin)
	gotStdin, ok := ElevationInput(ctx)
	if !ok || gotStdin != io.Reader(&stdin) {
		t.Fatal("elevation input was not preserved")
	}
	if _, ok := ElevationInput(nil); ok {
		t.Fatal("nil context unexpectedly contained elevation input")
	}
}
