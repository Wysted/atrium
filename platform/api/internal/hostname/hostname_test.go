package hostname

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	t.Parallel()
	label63 := strings.Repeat("a", MaxLabelLength)
	// 3 labels of 63 plus a short one: 63*3 + 3 dots + 59 = 251; build exactly 253 and 254 bytes.
	host253 := label63 + "." + label63 + "." + label63 + "." + strings.Repeat("b", 253-3*64)
	tests := []struct {
		host string
		want bool
	}{
		{"portal.apps.example.test", true},
		{"a", true},
		{"xn--bcher-kva.example", true},
		{"a-b.c0.example", true},
		{label63 + ".example", true},
		{host253, true},
		{host253 + "b", false},
		{label63 + "a.example", false},
		{"", false},
		{"Portal.example.test", false},
		{"portal.example.test.", false},
		{"*.example.test", false},
		{"a..example", false},
		{".example", false},
		{"-a.example", false},
		{"a-.example", false},
		{"a_b.example", false},
		{"example.test:8443", false},
		{"exa mple.test", false},
		{"example.test/", false},
		{"bücher.example", false},
		{"10.0.0.1", false},
		{"example.123", false},
		{"1example.test", true},
		{"a.example\x00", false},
		{"a.example\n", false},
	}
	for _, tt := range tests {
		if got := Valid(tt.host); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
	if len(host253) != MaxLength {
		t.Fatalf("test host has %d bytes, want %d", len(host253), MaxLength)
	}
}
