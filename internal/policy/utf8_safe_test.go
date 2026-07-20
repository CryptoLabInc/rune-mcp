package policy

import (
	"testing"
	"unicode/utf8"
)

func TestTruncRunes(t *testing.T) {
	cases := []struct {
		name    string
		s       string
		n       int
		want    string
		wantLen int // expected rune count
	}{
		{"empty input", "", 5, "", 0},
		{"zero cap", "hello", 0, "", 0},
		{"negative cap", "hello", -1, "", 0},
		{"ASCII under cap", "hi", 5, "hi", 2},
		{"ASCII at cap", "hello", 5, "hello", 5},
		{"ASCII over cap", "hello world", 5, "hello", 5},
		// 1 Korean char = 3 UTF-8 bytes
		{"Korean under cap", "안녕", 5, "안녕", 2},
		{"Korean exactly at cap", "안녕하세요", 5, "안녕하세요", 5},
		{"Korean over cap", "안녕하세요세계", 5, "안녕하세요", 5},
		// Mixed
		{"mixed under cap", "안녕 hi", 10, "안녕 hi", 5},
		{"mixed cut at korean boundary", "ab안녕cd", 3, "ab안", 3},
		{"mixed cut at ascii boundary", "안녕ab", 3, "안녕a", 3},
		// 1 emoji = 4 UTF-8 bytes (1 rune)
		{"emoji over cap", "🚀🎉🌟", 2, "🚀🎉", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncRunes(c.s, c.n)
			if got != c.want {
				t.Errorf("truncRunes(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncRunes(%q, %d) returned invalid UTF-8", c.s, c.n)
			}
			if utf8.RuneCountInString(got) != c.wantLen {
				t.Errorf("truncRunes(%q, %d) rune count = %d, want %d",
					c.s, c.n, utf8.RuneCountInString(got), c.wantLen)
			}
		})
	}
}
