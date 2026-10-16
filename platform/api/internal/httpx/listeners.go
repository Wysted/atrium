package httpx

import (
	"fmt"
	"net"
)

// NamedAddr is a listener address together with the setting that holds it, for error messages.
type NamedAddr struct {
	Name string
	Addr string
}

// ListenerConflicts validates every non-empty address as host:port and reports each pair that cannot bind
// together: same port with either host a wildcard ("", 0.0.0.0, ::) or both hosts equal.
func ListenerConflicts(addrs []NamedAddr) []error {
	type parsed struct {
		name, host, port string
	}
	var errs []error
	var ps []parsed
	for _, a := range addrs {
		if a.Addr == "" {
			continue // reported as missing by the caller
		}
		host, port, err := net.SplitHostPort(a.Addr)
		if err != nil || port == "" {
			errs = append(errs, fmt.Errorf("%s must be host:port, for example :8080: %q is not valid", a.Name, a.Addr))
			continue
		}
		ps = append(ps, parsed{a.Name, host, port})
	}
	for i := range ps {
		for j := i + 1; j < len(ps); j++ {
			if ps[i].port == ps[j].port && (isWildcard(ps[i].host) || isWildcard(ps[j].host) || ps[i].host == ps[j].host) {
				errs = append(errs, fmt.Errorf("%s and %s must use different ports (a wildcard host conflicts with any host on the same port)", ps[i].name, ps[j].name))
			}
		}
	}
	return errs
}

func isWildcard(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}
