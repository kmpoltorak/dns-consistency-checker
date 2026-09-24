// Package normalize converts DNS resource records into canonical, comparable
// values. The same code path normalizes resolver answers and user-supplied
// expected values, so both are always compared under identical rules.
package normalize

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

// Record is one normalized resource record.
type Record struct {
	Name  string // canonical owner name
	Type  string // record type mnemonic, e.g. "MX"
	Value string // canonical RDATA presentation; the comparison key
	TTL   uint32
	Raw   string // original presentation as produced by the DNS library
}

// normalizers maps each supported record type to its RDATA canonicalizer.
// Adding a record type means adding one entry here.
var normalizers = map[uint16]func(dns.RR) (string, error){
	dns.TypeA: func(rr dns.RR) (string, error) {
		addr, ok := netip.AddrFromSlice(rr.(*dns.A).A.To4())
		if !ok {
			return "", fmt.Errorf("invalid IPv4 address %q", rr.(*dns.A).A)
		}
		return addr.String(), nil
	},
	dns.TypeAAAA: func(rr dns.RR) (string, error) {
		ip := rr.(*dns.AAAA).AAAA
		if len(ip) != 16 {
			return "", fmt.Errorf("invalid IPv6 address %q", ip)
		}
		// Keep the 16-byte form: an IPv4-mapped AAAA stays "::ffff:a.b.c.d".
		return netip.AddrFrom16([16]byte(ip)).String(), nil
	},
	dns.TypeCNAME: func(rr dns.RR) (string, error) { return Name(rr.(*dns.CNAME).Target), nil },
	dns.TypeNS:    func(rr dns.RR) (string, error) { return Name(rr.(*dns.NS).Ns), nil },
	dns.TypePTR:   func(rr dns.RR) (string, error) { return Name(rr.(*dns.PTR).Ptr), nil },
	dns.TypeMX: func(rr dns.RR) (string, error) {
		mx := rr.(*dns.MX)
		return fmt.Sprintf("%d %s", mx.Preference, Name(mx.Mx)), nil
	},
	dns.TypeSRV: func(rr dns.RR) (string, error) {
		srv := rr.(*dns.SRV)
		return fmt.Sprintf("%d %d %d %s", srv.Priority, srv.Weight, srv.Port, Name(srv.Target)), nil
	},
	dns.TypeCAA: func(rr dns.RR) (string, error) {
		caa := rr.(*dns.CAA)
		// Tags are case-insensitive (RFC 8659 section 4.1); values are not.
		return fmt.Sprintf(`%d %s "%s"`, caa.Flag, strings.ToLower(caa.Tag), caa.Value), nil
	},
	dns.TypeSOA: func(rr dns.RR) (string, error) {
		soa := rr.(*dns.SOA)
		return fmt.Sprintf("%s %s %d %d %d %d %d", Name(soa.Ns), Name(soa.Mbox),
			soa.Serial, soa.Refresh, soa.Retry, soa.Expire, soa.Minttl), nil
	},
	dns.TypeTXT: func(rr dns.RR) (string, error) {
		// Character-string boundaries are preserved: "ab" and "a" "b" differ.
		// The library keeps strings in escaped presentation form (\" \\ \DDD),
		// which is canonical for values that came off the wire.
		return `"` + strings.Join(rr.(*dns.TXT).Txt, `" "`) + `"`, nil
	},
}

// SupportedTypes returns the supported record type mnemonics, sorted.
func SupportedTypes() []string {
	types := make([]string, 0, len(normalizers))
	for t := range normalizers {
		types = append(types, dns.TypeToString[t])
	}
	slices.Sort(types)
	return types
}

// ParseType validates a record type mnemonic (case-insensitive).
func ParseType(s string) (uint16, error) {
	t, ok := dns.StringToType[strings.ToUpper(strings.TrimSpace(s))]
	if _, supported := normalizers[t]; !ok || !supported {
		return 0, fmt.Errorf("unsupported record type %q (supported: %s)", s, strings.Join(SupportedTypes(), ", "))
	}
	return t, nil
}

// Name returns the canonical form of a domain name: ASCII lower-case and
// fully qualified with a trailing dot. Escaped bytes are left unchanged.
func Name(s string) string {
	return dns.CanonicalName(s)
}

// FromRR normalizes a resource record. ok is false for record types that are
// not supported (for example RRSIG), which callers skip.
func FromRR(rr dns.RR) (rec Record, ok bool, err error) {
	h := rr.Header()
	fn, ok := normalizers[h.Rrtype]
	if !ok {
		return Record{}, false, nil
	}
	value, err := fn(rr)
	if err != nil {
		return Record{}, true, err
	}
	return Record{
		Name:  Name(h.Name),
		Type:  dns.TypeToString[h.Rrtype],
		Value: value,
		TTL:   h.Ttl,
		Raw:   rr.String(),
	}, true, nil
}

// RRset sorts records by value (CompareValues) and removes duplicate values
// (an RRset is a set). The first occurrence of a duplicate is kept.
func RRset(records []Record) []Record {
	out := slices.Clone(records)
	slices.SortStableFunc(out, func(a, b Record) int { return CompareValues(a.Value, b.Value) })
	return slices.CompactFunc(out, func(a, b Record) bool { return a.Value == b.Value })
}

// CompareValues orders canonical values naturally: space-separated fields
// that are both numbers or both IP addresses compare numerically, so
// "5 mx." sorts before "10 mx." and 10.0.0.2 before 10.0.0.10. Ties fall back
// to byte order, making this a total order.
func CompareValues(a, b string) int {
	fa, fb := strings.Fields(a), strings.Fields(b)
	for i := range min(len(fa), len(fb)) {
		if c := compareField(fa[i], fb[i]); c != 0 {
			return c
		}
	}
	if c := len(fa) - len(fb); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func compareField(a, b string) int {
	if x, err := strconv.ParseUint(a, 10, 64); err == nil {
		if y, err := strconv.ParseUint(b, 10, 64); err == nil {
			return cmp.Compare(x, y)
		}
	}
	if x, err := netip.ParseAddr(a); err == nil {
		if y, err := netip.ParseAddr(b); err == nil {
			return x.Compare(y)
		}
	}
	return strings.Compare(a, b)
}

// ParseExpected parses an expected record value given in the presentation
// format of the record's RDATA (as in a zone file) and normalizes it with the
// same rules as resolver answers. Relative names are treated as fully
// qualified. A TXT value that does not start with a double quote is taken
// as one literal character string.
func ParseExpected(owner string, qtype uint16, value string) (Record, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Record{}, fmt.Errorf("empty expected value")
	}
	if qtype == dns.TypeTXT && !strings.HasPrefix(value, `"`) {
		value = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
	}
	typ := dns.TypeToString[qtype]
	rr, err := dns.NewRR(fmt.Sprintf("%s 0 IN %s %s", dns.Fqdn(owner), typ, value))
	if err != nil || rr == nil {
		return Record{}, fmt.Errorf("invalid expected %s value %q", typ, value)
	}
	// Round-trip through the wire format so escapes and text encodings end up
	// exactly as they do for resolver answers.
	buf := make([]byte, dns.MaxMsgSize)
	n, err := dns.PackRR(rr, buf, 0, nil, false)
	if err == nil {
		rr, _, err = dns.UnpackRR(buf[:n], 0)
	}
	if err != nil {
		return Record{}, fmt.Errorf("invalid expected %s value %q: %w", typ, value, err)
	}
	rec, _, err := FromRR(rr)
	if err != nil {
		return Record{}, fmt.Errorf("invalid expected %s value %q: %w", typ, value, err)
	}
	return rec, nil
}
