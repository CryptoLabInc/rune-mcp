//go:build !bench

// Package bench (production stub). The real instrumentation lives in bench_on.go,
// compiled only under `-tags bench`. This file is linked into every other build —
// including the production binary — so prod gets only the no-ops below: no
// time.Now(), no slog, no env reads. See bench_on.go for the design rationale.
package bench

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// Enabled is false at compile time, so `if bench.Enabled { ... }` call sites
// fold to dead code and the compiler drops their bench references (WithK,
// UnaryInterceptor, benchWrap) from the binary entirely.
const Enabled = false

// Now returns the zero Time without reading the clock. It inlines away, so no
// time.Now() syscall survives in production. Real impl: bench_on.go.
func Now() time.Time { return time.Time{} }

// Observe is a no-op. Its arguments are already-computed values at the call
// site, so the inlined empty body leaves nothing in the binary. Real impl:
// bench_on.go.
func Observe(context.Context, string, string, time.Time, error) {}

// WithK returns ctx unchanged. Real impl: bench_on.go.
func WithK(ctx context.Context, _ int) context.Context { return ctx }

// UnaryInterceptor returns nil. Its only caller sits inside `if bench.Enabled`,
// which is dead in this build; the declaration exists so that branch still
// type-checks. Real impl: bench_on.go.
func UnaryInterceptor(string) grpc.UnaryClientInterceptor { return nil }
