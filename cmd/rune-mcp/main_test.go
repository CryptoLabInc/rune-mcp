package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestHandleVersionFlag(t *testing.T) {
	prev := version
	version = "v0.0.0-test"
	defer func() { version = prev }()

	cases := []struct {
		name        string
		args        []string
		wantHandled bool
		wantOutput  string
	}{
		{"no args - main should continue", []string{"rune-mcp"}, false, ""},
		{"unrelated arg - main should continue", []string{"rune-mcp", "serve"}, false, ""},
		{"--version", []string{"rune-mcp", "--version"}, true, "v0.0.0-test"},
		{"-version", []string{"rune-mcp", "-version"}, true, "v0.0.0-test"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			got := handleVersionFlag(tc.args, &buf)
			if got != tc.wantHandled {
				t.Errorf("handled = %v, want %v", got, tc.wantHandled)
			}
			if tc.wantOutput != "" && !strings.Contains(buf.String(), tc.wantOutput) {
				t.Errorf("output = %q, want substring %q", buf.String(), tc.wantOutput)
			}
			if !tc.wantHandled && buf.Len() != 0 {
				t.Errorf("non-handled case wrote to stdout: %q", buf.String())
			}
		})
	}
}

func TestIsNormalShutdown(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil — stdin EOF surfaces here (SDK filters io.EOF to nil)", nil, true},
		{"context.Canceled — SIGINT/SIGTERM path", context.Canceled, true},
		{"wrapped context.Canceled", fmt.Errorf("wrap: %w", context.Canceled), true},
		{"raw io.EOF should NOT match — SDK already filters it", io.EOF, false},
		{"context.DeadlineExceeded is a real error in shutdown", context.DeadlineExceeded, false},
		{"arbitrary error", errors.New("transport broken"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNormalShutdown(tc.err); got != tc.want {
				t.Errorf("isNormalShutdown(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
