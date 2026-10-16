package testdb

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	t.Parallel()
	now := time.Unix(2_000_000_000, 0)
	at := func(age time.Duration) string {
		return namePrefix + strconv.FormatInt(now.Add(-age).Unix(), 10) + "_0123456789abcdef"
	}

	tests := []struct {
		name string
		db   string
		want bool
	}{
		{"exactly at the threshold is kept", at(staleAfter), false},
		{"one second past the threshold", at(staleAfter + time.Second), true},
		{"fresh", at(time.Minute), false},
		{"future timestamp", at(-time.Hour), false},
		{"template", templatePrefix + "1ebdd07fdadefa26", false},
		{"template with digits only", templatePrefix + "1234567890123456", false},
		{"legacy 16 hex name", namePrefix + "0123456789abcdef", false},
		{"legacy all-digit name", namePrefix + "1234567890123456", false},
		{"other database", "platform", false},
		{"uppercase suffix", namePrefix + "1_ABCDEF", false},
		{"empty suffix", namePrefix + "1_", false},
		{"overflowing timestamp", namePrefix + "99999999999999999999_ab", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isStale(tt.db, now); got != tt.want {
				t.Errorf("isStale(%q) = %v, want %v", tt.db, got, tt.want)
			}
		})
	}
}

func TestNewName(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	a, err := newName(now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newName(now)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two names collide: %q", a)
	}
	m := ephemeralName.FindStringSubmatch(a)
	if m == nil || m[1] != "1800000000" {
		t.Fatalf("name %q does not match %v with the creation time", a, ephemeralName)
	}
	if strings.HasPrefix(a, templatePrefix) {
		t.Fatalf("ephemeral name %q looks like a template", a)
	}
	if len(a) > 63 { // PostgreSQL identifier limit
		t.Fatalf("name %q is %d bytes, over the identifier limit", a, len(a))
	}
	if !regexp.MustCompile(`^platform_test_\d+_[0-9a-f]{16}$`).MatchString(a) {
		t.Fatalf("unexpected shape %q", a)
	}
}
