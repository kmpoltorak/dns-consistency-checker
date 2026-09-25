package dnsclient

import (
	"net/netip"
	"testing"
)

func TestParseResolver(t *testing.T) {
	tests := []struct {
		in, endpoint, address string
	}{
		{"8.8.8.8", "8.8.8.8:53", "8.8.8.8"},
		{" 8.8.8.8 ", "8.8.8.8:53", "8.8.8.8"},
		{"8.8.8.8:53", "8.8.8.8:53", "8.8.8.8"},
		{"127.0.0.1:5353", "127.0.0.1:5353", "127.0.0.1:5353"},
		{"2001:4860:4860::8888", "[2001:4860:4860::8888]:53", "2001:4860:4860::8888"},
		{"[2001:4860:4860::8888]", "[2001:4860:4860::8888]:53", "2001:4860:4860::8888"},
		{"[2001:4860:4860::8888]:53", "[2001:4860:4860::8888]:53", "2001:4860:4860::8888"},
		{"[2001:4860:4860:0:0:0:0:8888]:5353", "[2001:4860:4860::8888]:5353", "[2001:4860:4860::8888]:5353"},
		{"::1", "[::1]:53", "::1"},
		{"::ffff:1.1.1.1", "1.1.1.1:53", "1.1.1.1"},
		{"[fe80::1%en0]:53", "[fe80::1%en0]:53", "fe80::1%en0"},
	}
	for _, tt := range tests {
		r, err := ParseResolver("", tt.in)
		if err != nil {
			t.Errorf("ParseResolver(%q): %v", tt.in, err)
			continue
		}
		if got := r.Endpoint.String(); got != tt.endpoint {
			t.Errorf("ParseResolver(%q) endpoint = %q, want %q", tt.in, got, tt.endpoint)
		}
		if got := r.Address(); got != tt.address {
			t.Errorf("ParseResolver(%q) address = %q, want %q", tt.in, got, tt.address)
		}
	}
}

func TestParseResolverInvalid(t *testing.T) {
	for _, in := range []string{
		"", "   ", "bad host", "host!", "-bad..host", "dns.google:0", "dns.google:dns", "dns.google:70000", "[dns.google]:53", "8.8.8", "8.8.8.8:", "8.8.8.8:0", "8.8.8.8:65536", "8.8.8.8:-1",
		"8.8.8.8:dns", "[8.8.8.8]", "[8.8.8.8]:53", "2001:db8::1:53x", "[2001:db8::1", "0.0.0.0", "::",
		"1.2.3.4:53:53", "256.1.1.1",
	} {
		if r, err := ParseResolver("", in); err == nil {
			t.Errorf("ParseResolver(%q) = %v, want error", in, r.Endpoint)
		}
	}
}

func TestResolverString(t *testing.T) {
	r, _ := ParseResolver(" cloudflare ", "1.1.1.1")
	if got := r.String(); got != "cloudflare (1.1.1.1)" {
		t.Errorf("String() = %q", got)
	}
	r, _ = ParseResolver("", "[2001:db8::1]:5353")
	if got := r.String(); got != "[2001:db8::1]:5353" {
		t.Errorf("String() = %q", got)
	}
}

func TestParseResolverHostname(t *testing.T) {
	tests := []struct{ in, host, address, key string }{
		{"dns.google", "dns.google", "dns.google", "dns.google:53"},
		{"DNS.Google.", "dns.google", "dns.google", "dns.google:53"},
		{"dns.google:53", "dns.google", "dns.google", "dns.google:53"},
		{"one.one.one.one:5353", "one.one.one.one", "one.one.one.one:5353", "one.one.one.one:5353"},
		{"localhost", "localhost", "localhost", "localhost:53"},
		{"_dns.resolver.arpa", "_dns.resolver.arpa", "_dns.resolver.arpa", "_dns.resolver.arpa:53"},
	}
	for _, tt := range tests {
		r, err := ParseResolver("", tt.in)
		if err != nil {
			t.Errorf("ParseResolver(%q): %v", tt.in, err)
			continue
		}
		if r.Host != tt.host || r.Address() != tt.address || r.Key() != tt.key || r.Resolved() {
			t.Errorf("ParseResolver(%q) = host %q address %q key %q resolved %v", tt.in, r.Host, r.Address(), r.Key(), r.Resolved())
		}
	}
	// IP literals are never treated as hostnames.
	if r, _ := ParseResolver("", "8.8.8.8"); r.Host != "" || r.Key() != "8.8.8.8:53" {
		t.Errorf("IP parsed as hostname: %+v", r)
	}
}

func TestResolverHostnameDisplay(t *testing.T) {
	r, _ := ParseResolver("google", "dns.google")
	if r.String() != "google (dns.google)" {
		t.Errorf("unresolved String() = %q", r.String())
	}
	r.Endpoint = netip.AddrPortFrom(netip.MustParseAddr("8.8.4.4"), 53)
	if r.Address() != "dns.google (8.8.4.4)" || r.String() != "google (dns.google, 8.8.4.4)" {
		t.Errorf("resolved: Address() = %q String() = %q", r.Address(), r.String())
	}
	r.Name = ""
	if r.String() != "dns.google (8.8.4.4)" {
		t.Errorf("unnamed String() = %q", r.String())
	}
}
