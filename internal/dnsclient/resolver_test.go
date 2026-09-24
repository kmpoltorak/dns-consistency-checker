package dnsclient

import "testing"

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
		"", "   ", "dns.google", "8.8.8", "8.8.8.8:", "8.8.8.8:0", "8.8.8.8:65536", "8.8.8.8:-1",
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
