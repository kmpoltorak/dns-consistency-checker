package normalize

import (
	"net"
	"slices"
	"testing"

	"github.com/miekg/dns"
)

func mustRR(t testing.TB, s string) dns.RR {
	t.Helper()
	rr, err := dns.NewRR(s)
	if err != nil {
		t.Fatalf("NewRR(%q): %v", s, err)
	}
	return rr
}

// wire round-trips an RR through the wire format so tests see exactly what a
// resolver answer looks like after unpacking.
func wire(t testing.TB, rr dns.RR) dns.RR {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)
	m.Answer = []dns.RR{rr}
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Unpack(b); err != nil {
		t.Fatal(err)
	}
	return m.Answer[0]
}

func TestFromRR(t *testing.T) {
	tests := []struct {
		name, rr, want string
	}{
		{"A", "Example.COM. 300 IN A 10.0.0.1", "10.0.0.1"},
		{"AAAA full form", "example.com. 300 IN AAAA 2001:0db8:0000:0000:0000:0000:0000:0001", "2001:db8::1"},
		{"AAAA upper", "example.com. 300 IN AAAA 2001:DB8::A", "2001:db8::a"},
		{"AAAA v4-mapped stays mapped", "example.com. 300 IN AAAA ::ffff:10.0.0.1", "::ffff:10.0.0.1"},
		{"CNAME case and dot", "www.example.com. 300 IN CNAME Frontend.Example.NET.", "frontend.example.net."},
		{"NS", "example.com. 300 IN NS NS1.Example.com.", "ns1.example.com."},
		{"PTR", "1.0.0.10.in-addr.arpa. 300 IN PTR Host.Example.com.", "host.example.com."},
		{"MX", "example.com. 300 IN MX 10 MAIL.example.com.", "10 mail.example.com."},
		{"SRV", "_sip._tcp.example.com. 300 IN SRV 10 60 5060 SIP.example.com.", "10 60 5060 sip.example.com."},
		{"CAA tag lowercased value kept", `example.com. 300 IN CAA 0 ISSUE "LetsEncrypt.org"`, `0 issue "LetsEncrypt.org"`},
		{"CAA critical flag", `example.com. 300 IN CAA 128 iodef "mailto:a@example.com"`, `128 iodef "mailto:a@example.com"`},
		{"SOA", "example.com. 300 IN SOA NS1.example.com. Hostmaster.Example.com. 2024010101 7200 3600 1209600 300",
			"ns1.example.com. hostmaster.example.com. 2024010101 7200 3600 1209600 300"},
		{"TXT single", `example.com. 300 IN TXT "v=spf1 -all"`, `"v=spf1 -all"`},
		{"TXT multiple strings kept apart", `example.com. 300 IN TXT "abc" "def"`, `"abc" "def"`},
		{"TXT quote inside", `example.com. 300 IN TXT "a\"b"`, `"a\"b"`},
		{"TXT empty string", `example.com. 300 IN TXT ""`, `""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, ok, err := FromRR(wire(t, mustRR(t, tt.rr)))
			if err != nil || !ok {
				t.Fatalf("FromRR: ok=%v err=%v", ok, err)
			}
			if rec.Value != tt.want {
				t.Errorf("Value = %q, want %q", rec.Value, tt.want)
			}
			if rec.TTL != 300 {
				t.Errorf("TTL = %d, want 300", rec.TTL)
			}
			if rec.Name != Name(rec.Name) {
				t.Errorf("owner %q not canonical", rec.Name)
			}
		})
	}
}

func TestTXTStringBoundariesDiffer(t *testing.T) {
	a, _, _ := FromRR(mustRR(t, `example.com. 1 IN TXT "ab"`))
	b, _, _ := FromRR(mustRR(t, `example.com. 1 IN TXT "a" "b"`))
	if a.Value == b.Value {
		t.Fatalf("%q and %q must not be equal", a.Value, b.Value)
	}
}

func TestFromRRUnsupported(t *testing.T) {
	_, ok, err := FromRR(mustRR(t, "example.com. 300 IN HINFO cpu os"))
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v, want unsupported", ok, err)
	}
}

func TestFromRRInvalidAddress(t *testing.T) {
	if _, _, err := FromRR(&dns.A{Hdr: dns.RR_Header{Name: "a.", Rrtype: dns.TypeA}, A: net.IP{1, 2}}); err == nil {
		t.Fatal("expected error for malformed A")
	}
}

func TestName(t *testing.T) {
	for in, want := range map[string]string{
		"Example.COM":   "example.com.",
		"example.com.":  "example.com.",
		"_LDAP._tcp.x":  "_ldap._tcp.x.",
		".":             ".",
		`a\065.example`: `a\065.example.`,
	} {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseType(t *testing.T) {
	for _, s := range []string{"A", "aaaa", " mx ", "CNAME", "NS", "TXT", "PTR", "SRV", "CAA", "SOA"} {
		if _, err := ParseType(s); err != nil {
			t.Errorf("ParseType(%q): %v", s, err)
		}
	}
	for _, s := range []string{"", "ANY", "HINFO", "XYZ", "RRSIG"} {
		if _, err := ParseType(s); err == nil {
			t.Errorf("ParseType(%q) accepted", s)
		}
	}
	if got := len(SupportedTypes()); got != 10 {
		t.Errorf("SupportedTypes() len = %d, want 10", got)
	}
}

func TestRRsetSortsAndDeduplicates(t *testing.T) {
	in := []Record{{Value: "10.0.0.2", TTL: 1}, {Value: "10.0.0.1", TTL: 2}, {Value: "10.0.0.2", TTL: 3}}
	got := RRset(in)
	if len(got) != 2 || got[0].Value != "10.0.0.1" || got[1].Value != "10.0.0.2" || got[1].TTL != 1 {
		t.Fatalf("RRset = %+v", got)
	}
	if in[0].Value != "10.0.0.2" {
		t.Fatal("RRset modified its input")
	}
}

func TestParseExpected(t *testing.T) {
	tests := []struct {
		typ   uint16
		value string
		want  string
	}{
		{dns.TypeA, "10.20.30.40", "10.20.30.40"},
		{dns.TypeAAAA, "2001:DB8:0::1", "2001:db8::1"},
		{dns.TypeCNAME, "Edge.Example.net", "edge.example.net."},
		{dns.TypeNS, "ns1.example.com.", "ns1.example.com."},
		{dns.TypePTR, "host.example.com", "host.example.com."},
		{dns.TypeMX, "10 Mail.example.com", "10 mail.example.com."},
		{dns.TypeSRV, "10 60 5060 sip.example.com.", "10 60 5060 sip.example.com."},
		{dns.TypeCAA, `0 issue "letsencrypt.org"`, `0 issue "letsencrypt.org"`},
		{dns.TypeSOA, "ns1.example.com. hostmaster.example.com. 1 7200 3600 1209600 300",
			"ns1.example.com. hostmaster.example.com. 1 7200 3600 1209600 300"},
		{dns.TypeTXT, "v=spf1 include:example.net -all", `"v=spf1 include:example.net -all"`},
		{dns.TypeTXT, `"part one" "part two"`, `"part one" "part two"`},
		{dns.TypeTXT, `"\065b\"c"`, `"Ab\"c"`},
		{dns.TypeTXT, `say "hi" \ zażółć`, `"say \"hi\" \\ za\197\188\195\179\197\130\196\135"`},
		{dns.TypeCAA, `0 issuewild ";"`, `0 issuewild ";"`},
	}
	for _, tt := range tests {
		rec, err := ParseExpected("example.com", tt.typ, tt.value)
		if err != nil {
			t.Errorf("ParseExpected(%s, %q): %v", dns.TypeToString[tt.typ], tt.value, err)
			continue
		}
		if rec.Value != tt.want {
			t.Errorf("ParseExpected(%s, %q) = %q, want %q", dns.TypeToString[tt.typ], tt.value, rec.Value, tt.want)
		}
	}
	for _, bad := range []struct {
		typ   uint16
		value string
	}{
		{dns.TypeA, "not-an-ip"},
		{dns.TypeA, "2001:db8::1"},
		{dns.TypeAAAA, "10.0.0.1x"},
		{dns.TypeMX, "mail.example.com"},
		{dns.TypeSRV, "10 60 sip.example.com."},
		{dns.TypeA, "  "},
	} {
		if _, err := ParseExpected("example.com", bad.typ, bad.value); err == nil {
			t.Errorf("ParseExpected(%s, %q) accepted", dns.TypeToString[bad.typ], bad.value)
		}
	}
}

// Expected values and wire answers must normalize identically.
func TestParseExpectedMatchesWire(t *testing.T) {
	exp, err := ParseExpected("example.com", dns.TypeMX, "10 MAIL.example.com")
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := FromRR(wire(t, mustRR(t, "example.com. 60 IN MX 10 mail.EXAMPLE.com.")))
	if exp.Value != got.Value {
		t.Fatalf("expected %q != wire %q", exp.Value, got.Value)
	}
}

func BenchmarkNormalizeRRset(b *testing.B) {
	rrs := []dns.RR{
		mustRR(b, "example.com. 300 IN A 10.0.0.4"),
		mustRR(b, "example.com. 300 IN A 10.0.0.1"),
		mustRR(b, "example.com. 300 IN A 10.0.0.3"),
		mustRR(b, "example.com. 300 IN A 10.0.0.2"),
		mustRR(b, `example.com. 300 IN TXT "v=spf1 -all" "second"`),
		mustRR(b, "example.com. 300 IN MX 10 mail.example.com."),
	}
	b.ReportAllocs()
	for b.Loop() {
		recs := make([]Record, 0, len(rrs))
		for _, rr := range rrs {
			r, _, _ := FromRR(rr)
			recs = append(recs, r)
		}
		_ = RRset(recs)
	}
}

func TestSupportedTypesSorted(t *testing.T) {
	if !slices.IsSorted(SupportedTypes()) {
		t.Fatal("not sorted")
	}
}

func TestCompareValues(t *testing.T) {
	sorted := [][]string{
		{"10.0.0.2", "10.0.0.10", "192.0.2.1"},
		{"::1", "2001:db8::2", "2001:db8::10"},
		{"5 gmail.com.", "10 alt1.gmail.com.", "10 alt2.gmail.com.", "40 alt4.gmail.com."},
		{"a.example.", "b.example."},
		{`"a"`, `"a" "b"`, `"b"`},
	}
	for _, want := range sorted {
		got := slices.Clone(want)
		slices.Reverse(got)
		slices.SortFunc(got, CompareValues)
		if !slices.Equal(got, want) {
			t.Errorf("sorted = %q, want %q", got, want)
		}
	}
}
