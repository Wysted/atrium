// Package hostname validates the host names the platform stores and matches: deployments.host, the unknown_host
// denials of ForwardAuth and the portal API must agree on one definition.
package hostname

import "strings"

// Limits of a host (RFC 1035 and RFC 1123).
const (
	MaxLength      = 253
	MaxLabelLength = 63
)

// Valid reports whether host is a lowercase DNS name that can be matched against X-Forwarded-Host: at most
// 253 bytes, labels of 1 to 63 characters from [a-z0-9-] that neither start nor end with a hyphen, no trailing
// dot, no wildcard, no port. A name whose last label is all digits is refused too: it would be an IP address
// (or look like one), and Traefik routes by name. Internationalized names must come in their xn-- form. Every
// valid host satisfies the CHECK of deployments.host.
func Valid(host string) bool {
	if host == "" || len(host) > MaxLength {
		return false
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" || len(l) > MaxLabelLength || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := range len(l) {
			if c := l[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return !allDigits(labels[len(labels)-1])
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
