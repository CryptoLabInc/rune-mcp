//go:build !bench

package bench

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// The production build (no -tags bench) links the no-op stubs in bench_off.go.
// These tests pin the "off = nothing happens" contract that lets every call
// site stay unguarded-by-runtime: the build tag is the only switch.

// Enabled must be the false constant so `if bench.Enabled` call sites fold to
// dead code and drop their bench references from the production binary.
func TestEnabled_FalseInProdBuild(t *testing.T) {
	if Enabled {
		t.Fatal("Enabled must be false without -tags bench")
	}
}

// Now must return the zero Time — no clock is read in the production build.
func TestNow_ReturnsZeroInProdBuild(t *testing.T) {
	if !Now().IsZero() {
		t.Fatal("Now must return the zero Time in the production build")
	}
}

// Observe must emit nothing. This is the safety net: even if a call site forgot
// every guard, the production binary produces zero bench log noise.
func TestObserve_OffIsNoOp(t *testing.T) {
	var sb strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&sb, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	Observe(context.Background(), "envector", "/envector/Score", Now(), nil)

	if got := sb.String(); got != "" {
		t.Fatalf("off must emit nothing, got: %q", got)
	}
}

// WithK must pass ctx through untouched (the k= tag only exists in the bench build).
func TestWithK_PassThroughInProdBuild(t *testing.T) {
	ctx := context.Background()
	if WithK(ctx, 20) != ctx {
		t.Fatal("WithK must return ctx unchanged in the production build")
	}
}

// UnaryInterceptor must be nil so withBench appends nothing in the production build.
func TestUnaryInterceptor_NilInProdBuild(t *testing.T) {
	if UnaryInterceptor("envector") != nil {
		t.Fatal("UnaryInterceptor must be nil in the production build")
	}
}
