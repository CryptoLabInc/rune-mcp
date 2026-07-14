package console

import (
	"strings"
	"testing"
)

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"tcp scheme with port", "tcp://console.example.com:50051", "console.example.com:50051"},
		{"tcp scheme without port", "tcp://console.example.com", "console.example.com:50051"},
		{"http scheme with port", "http://console.example.com:8080", "console.example.com:8080"},
		{"http scheme without port", "http://console.example.com", "console.example.com:50051"},
		{"https scheme with port and path", "https://console.example.com:443/api", "console.example.com:443"},
		{"https scheme without port", "https://console.example.com", "console.example.com:50051"},
		{"bare host:port", "console.example.com:50051", "console.example.com:50051"},
		{"bare host without port", "console.example.com", "console.example.com:50051"},
		{"loopback IPv4", "127.0.0.1:50051", "127.0.0.1:50051"},
		{"IPv6 with port via tcp", "tcp://[::1]:50051", "[::1]:50051"},
		{"IPv6 without port via tcp", "tcp://[::1]", "[::1]:50051"},
		{"trims surrounding whitespace", "  tcp://console.example.com:50051  ", "console.example.com:50051"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeEndpoint(c.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("NormalizeEndpoint(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeEndpoint_Errors(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantCode  string
		wantInMsg string
	}{
		{"empty", "", "CONSOLE_BAD_ENDPOINT", "empty"},
		{"whitespace only", "   ", "CONSOLE_BAD_ENDPOINT", "empty"},
		{"scheme only", "tcp://", "CONSOLE_BAD_ENDPOINT", "missing host"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NormalizeEndpoint(c.in)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", c.in)
			}
			ve, ok := err.(*Error)
			if !ok {
				t.Fatalf("expected *console.Error, got %T", err)
			}
			if ve.Code != c.wantCode {
				t.Fatalf("Code = %q, want %q", ve.Code, c.wantCode)
			}
			if !strings.Contains(ve.Message, c.wantInMsg) {
				t.Fatalf("Message = %q, want substring %q", ve.Message, c.wantInMsg)
			}
		})
	}
}
