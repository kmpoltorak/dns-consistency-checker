package dnsclient

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultPort is used when a resolver address has no port.
const DefaultPort = 53

// Resolver is a DNS server endpoint with an optional display name. A
// resolver given by hostname has Host set; its Endpoint carries the port and
// gets an address once the hostname is resolved.
type Resolver struct {
	Name     string
	Host     string // hostname as given (lower-case, no trailing dot); empty for IP literals
	Endpoint netip.AddrPort
}

// ParseResolver parses a resolver address. Accepted forms:
//
//	8.8.8.8
//	8.8.8.8:53
//	2001:4860:4860::8888
//	[2001:4860:4860::8888]
//	[2001:4860:4860::8888]:53
//	dns.google
//	dns.google:53
//
// IPv4-mapped IPv6 addresses are converted to IPv4. Hostnames are resolved
// with the system resolver when the resolver is queried.
func ParseResolver(name, address string) (Resolver, error) {
	s := strings.TrimSpace(address)
	if s == "" {
		return Resolver{}, fmt.Errorf("empty resolver address")
	}
	name = strings.TrimSpace(name)
	var ap netip.AddrPort
	if addr, err := netip.ParseAddr(s); err == nil {
		ap = netip.AddrPortFrom(addr, DefaultPort)
	} else if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		addr, err := netip.ParseAddr(s[1 : len(s)-1])
		if err != nil || !addr.Is6() {
			return Resolver{}, fmt.Errorf("invalid resolver address %q: not an IPv6 address", address)
		}
		ap = netip.AddrPortFrom(addr, DefaultPort)
	} else if ap, err = netip.ParseAddrPort(s); err != nil {
		return parseHostResolver(name, address, s)
	}
	if ap.Port() == 0 {
		return Resolver{}, fmt.Errorf("invalid resolver address %q: port must be 1-65535", address)
	}
	if !ap.Addr().IsValid() || ap.Addr().IsUnspecified() {
		return Resolver{}, fmt.Errorf("invalid resolver address %q: unspecified address", address)
	}
	return Resolver{Name: name, Endpoint: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}, nil
}

// parseHostResolver parses "HOST" or "HOST:PORT".
func parseHostResolver(name, address, s string) (Resolver, error) {
	invalid := fmt.Errorf("invalid resolver address %q: expected IP, IP:PORT, [IPv6]:PORT, HOSTNAME or HOSTNAME:PORT", address)
	host, port := s, uint64(DefaultPort)
	if strings.Contains(s, ":") {
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			return Resolver{}, invalid
		}
		if port, err = strconv.ParseUint(p, 10, 16); err != nil || port == 0 {
			return Resolver{}, fmt.Errorf("invalid resolver address %q: port must be 1-65535", address)
		}
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !validHostname(host) || strings.HasPrefix(s, "[") {
		return Resolver{}, invalid
	}
	return Resolver{Name: name, Host: host, Endpoint: netip.AddrPortFrom(netip.Addr{}, uint16(port))}, nil
}

// validHostname accepts LDH labels (plus "_", used in service names) of
// 1-63 characters, at most 253 characters in total. A numeric last label is a
// mistyped IP address (8.8.8, 256.1.1.1): top-level domains are never numeric.
func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return false
			}
		}
	}
	_, err := strconv.Atoi(labels[len(labels)-1])
	return err != nil
}

// Key identifies the endpoint for de-duplication: IP:PORT, or HOST:PORT for
// resolvers given by hostname.
func (r Resolver) Key() string {
	if r.Host != "" {
		return net.JoinHostPort(r.Host, strconv.Itoa(int(r.Endpoint.Port())))
	}
	return r.Endpoint.String()
}

// Resolved reports whether the resolver has an IP address to query.
func (r Resolver) Resolved() bool { return r.Endpoint.Addr().IsValid() }

// hostPort returns the hostname with ":PORT" unless the port is 53.
func (r Resolver) hostPort() string {
	if r.Endpoint.Port() == DefaultPort {
		return r.Host
	}
	return net.JoinHostPort(r.Host, strconv.Itoa(int(r.Endpoint.Port())))
}

// Address returns the endpoint for display: the bare IP when the port is 53,
// otherwise IP:PORT or [IPv6]:PORT. Hostnames are shown as given, followed
// by the resolved IP in parentheses once known.
func (r Resolver) Address() string {
	switch {
	case r.Host != "" && r.Resolved():
		return r.hostPort() + " (" + r.Endpoint.Addr().String() + ")"
	case r.Host != "":
		return r.hostPort()
	case r.Endpoint.Port() == DefaultPort:
		return r.Endpoint.Addr().String()
	}
	return r.Endpoint.String()
}

// String returns "name (address)" when a name is set, otherwise the address.
func (r Resolver) String() string {
	switch {
	case r.Name == "":
		return r.Address()
	case r.Host != "" && r.Resolved():
		return r.Name + " (" + r.hostPort() + ", " + r.Endpoint.Addr().String() + ")"
	}
	return r.Name + " (" + r.Address() + ")"
}

// resolveHost looks up the resolver's hostname with the system resolver and
// returns a copy with the chosen address. IPv4 is preferred (it is reachable
// on more networks); among addresses of the same family the lowest wins, so
// the choice is deterministic.
func (r Resolver) resolveHost(ctx context.Context, timeout time.Duration) (Resolver, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", r.Host)
	if err != nil {
		return r, fmt.Errorf("cannot resolve resolver hostname %s: %w", r.Host, err)
	}
	for i := range addrs {
		addrs[i] = addrs[i].Unmap()
	}
	addrs = slices.DeleteFunc(addrs, func(a netip.Addr) bool { return !a.IsValid() || a.IsUnspecified() })
	if len(addrs) == 0 {
		return r, fmt.Errorf("cannot resolve resolver hostname %s: no addresses", r.Host)
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int {
		if a.Is4() != b.Is4() {
			if a.Is4() {
				return -1
			}
			return 1
		}
		return a.Compare(b)
	})
	r.Endpoint = netip.AddrPortFrom(addrs[0], r.Endpoint.Port())
	return r, nil
}
