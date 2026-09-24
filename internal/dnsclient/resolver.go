package dnsclient

import (
	"fmt"
	"net/netip"
	"strings"
)

// DefaultPort is used when a resolver address has no port.
const DefaultPort = 53

// Resolver is a DNS server endpoint with an optional display name.
type Resolver struct {
	Name     string
	Endpoint netip.AddrPort
}

// ParseResolver parses a resolver address. Accepted forms:
//
//	8.8.8.8
//	8.8.8.8:53
//	2001:4860:4860::8888
//	[2001:4860:4860::8888]
//	[2001:4860:4860::8888]:53
//
// Only IP literals are accepted so that checking DNS never depends on DNS.
// IPv4-mapped IPv6 addresses are converted to IPv4.
func ParseResolver(name, address string) (Resolver, error) {
	s := strings.TrimSpace(address)
	if s == "" {
		return Resolver{}, fmt.Errorf("empty resolver address")
	}
	var ap netip.AddrPort
	if addr, err := netip.ParseAddr(s); err == nil {
		ap = netip.AddrPortFrom(addr, DefaultPort)
	} else if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		addr, err := netip.ParseAddr(s[1 : len(s)-1])
		if err != nil || !addr.Is6() {
			return Resolver{}, fmt.Errorf("invalid resolver address %q: not an IPv6 address", address)
		}
		ap = netip.AddrPortFrom(addr, DefaultPort)
	} else {
		ap, err = netip.ParseAddrPort(s)
		if err != nil {
			return Resolver{}, fmt.Errorf("invalid resolver address %q: expected IP, IP:PORT or [IPv6]:PORT", address)
		}
		if ap.Port() == 0 {
			return Resolver{}, fmt.Errorf("invalid resolver address %q: port must be 1-65535", address)
		}
	}
	if !ap.Addr().IsValid() || ap.Addr().IsUnspecified() {
		return Resolver{}, fmt.Errorf("invalid resolver address %q: unspecified address", address)
	}
	return Resolver{
		Name:     strings.TrimSpace(name),
		Endpoint: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()),
	}, nil
}

// Address returns the endpoint for display: the bare IP when the port is 53,
// otherwise IP:PORT or [IPv6]:PORT.
func (r Resolver) Address() string {
	if r.Endpoint.Port() == DefaultPort {
		return r.Endpoint.Addr().String()
	}
	return r.Endpoint.String()
}

// String returns "name (address)" when a name is set, otherwise the address.
func (r Resolver) String() string {
	if r.Name == "" {
		return r.Address()
	}
	return r.Name + " (" + r.Address() + ")"
}
