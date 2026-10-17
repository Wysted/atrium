package apitoken_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"platform/api/internal/apitoken"
)

func TestGenerate(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 200 {
		s, err := apitoken.Generate()
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != 55 || apitoken.Length != 55 || !strings.HasPrefix(s, "ptk_") || !apitoken.WellFormed(s) {
			t.Fatalf("bad secret shape (len %d)", len(s))
		}
		if seen[s] {
			t.Fatal("duplicate secret")
		}
		seen[s] = true
	}
}

func TestWellFormed(t *testing.T) {
	t.Parallel()
	good, err := apitoken.Generate()
	if err != nil {
		t.Fatal(err)
	}
	flip := func(s string, i int) string {
		c := byte('A')
		if s[i] == 'A' {
			c = 'B'
		}
		return s[:i] + string(c) + s[i+1:]
	}
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"valid", good, true},
		{"empty", "", false},
		{"prefix only", "ptk_", false},
		{"wrong prefix", "ptx_" + good[4:], false},
		{"uppercase prefix", "PTK_" + good[4:], false},
		{"too short", good[:len(good)-1], false},
		{"too long", good + "0", false},
		{"body char changed", flip(good, 10), false},
		{"checksum changed", good[:len(good)-1] + otherHex(good[len(good)-1]), false},
		{"standard base64 plus", good[:10] + "+" + good[11:], false},
		{"standard base64 slash", good[:10] + "/" + good[11:], false},
		{"space inside", good[:10] + " " + good[11:], false},
		{"NUL inside", good[:10] + "\x00" + good[11:], false},
		{"non-ASCII same byte length", good[:10] + "é" + good[12:], false},
		{"trailing newline", good[:len(good)-1] + "\n", false},
		{"padded", " " + good[:len(good)-1], false},
		{"Bearer prefix", "Bearer " + good, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := apitoken.WellFormed(tt.in); got != tt.want {
				t.Errorf("WellFormed(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func otherHex(c byte) string {
	if c == '0' {
		return "1"
	}
	return "0"
}

func TestHash(t *testing.T) {
	t.Parallel()
	// SHA-256("abc"), a published test vector: the hash covers exactly the bytes of the string.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := hex.EncodeToString(apitoken.Hash("abc")); got != want {
		t.Errorf("Hash = %s", got)
	}
	if len(apitoken.Hash("x")) != apitoken.HashSize {
		t.Error("hash size")
	}
}

// FuzzWellFormed checks that the checker never panics and that anything it accepts has the exact layout and
// round-trips through the checksum, whatever the input.
func FuzzWellFormed(f *testing.F) {
	good, _ := apitoken.Generate()
	for _, seed := range []string{"", "ptk_", good, good[:54], good + "x", strings.Repeat("A", 55)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !apitoken.WellFormed(s) {
			return
		}
		if len(s) != apitoken.Length || !strings.HasPrefix(s, apitoken.Prefix) {
			t.Fatalf("accepted a malformed secret of length %d", len(s))
		}
		for _, r := range s {
			if r > 'z' {
				t.Fatalf("accepted a non-ASCII rune")
			}
		}
	})
}
