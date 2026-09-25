package dnsclient

import (
	"context"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/kmpoltorak/dns-consistency-checker/internal/testdns"
)

func opts() Options {
	return Options{Protocol: UDP, TCPFallback: true, Timeout: 500 * time.Millisecond, Concurrency: 10}
}

func resolver(t testing.TB, addr string) Resolver {
	t.Helper()
	r, err := ParseResolver("", addr)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func values(r Result) []string {
	var out []string
	for _, rec := range r.Records {
		out = append(out, rec.Value)
	}
	return out
}

func TestQueryRecordTypes(t *testing.T) {
	tests := []struct {
		qtype  uint16
		qname  string
		record string
		want   string
	}{
		{dns.TypeA, "example.com", "example.com. 300 IN A 10.0.0.1", "10.0.0.1"},
		{dns.TypeAAAA, "example.com", "example.com. 300 IN AAAA 2001:db8::1", "2001:db8::1"},
		{dns.TypeCNAME, "www.example.com", "www.example.com. 300 IN CNAME Edge.example.net.", "edge.example.net."},
		{dns.TypeMX, "example.com", "example.com. 300 IN MX 10 mail.example.com.", "10 mail.example.com."},
		{dns.TypeNS, "example.com", "example.com. 300 IN NS ns1.example.com.", "ns1.example.com."},
		{dns.TypeTXT, "example.com", `example.com. 300 IN TXT "v=spf1" "-all"`, `"v=spf1" "-all"`},
		{dns.TypePTR, "1.0.0.10.in-addr.arpa", "1.0.0.10.in-addr.arpa. 300 IN PTR host.example.com.", "host.example.com."},
		{dns.TypeSRV, "_sip._tcp.example.com", "_sip._tcp.example.com. 300 IN SRV 10 60 5060 sip.example.com.", "10 60 5060 sip.example.com."},
		{dns.TypeCAA, "example.com", `example.com. 300 IN CAA 0 issue "ca.example.net"`, `0 issue "ca.example.net"`},
		{dns.TypeSOA, "example.com", "example.com. 300 IN SOA ns1.example.com. host.example.com. 1 2 3 4 5", "ns1.example.com. host.example.com. 1 2 3 4 5"},
	}
	for _, tt := range tests {
		t.Run(dns.TypeToString[tt.qtype], func(t *testing.T) {
			srv := testdns.Start(t, testdns.Reply(dns.RcodeSuccess, tt.record))
			res := Query(context.Background(), resolver(t, srv.Addr), tt.qname, tt.qtype, opts())
			if res.Status != StatusNoError {
				t.Fatalf("status = %s (%s)", res.Status, res.Error)
			}
			if got := values(res); !slices.Equal(got, []string{tt.want}) {
				t.Fatalf("records = %q, want %q", got, tt.want)
			}
			if res.Records[0].TTL != 300 || res.Attempts != 1 || res.ProtocolFinal != UDP {
				t.Fatalf("unexpected result %+v", res)
			}
			if !res.Flags.RecursionAvailable || !res.Flags.RecursionDesired {
				t.Fatalf("flags = %+v", res.Flags)
			}
		})
	}
}

func TestQueryMultipleRecordsSortedAndDeduplicated(t *testing.T) {
	srv := testdns.Start(t, testdns.Reply(dns.RcodeSuccess,
		"example.com. 300 IN A 10.0.0.2", "example.com. 300 IN A 10.0.0.1", "example.com. 300 IN A 10.0.0.2"))
	res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeA, opts())
	if got := values(res); !slices.Equal(got, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Fatalf("records = %q", got)
	}
}

func TestQueryRcodes(t *testing.T) {
	for rcode, want := range map[int]Status{
		dns.RcodeSuccess:        StatusNoError,
		dns.RcodeNameError:      StatusNXDomain,
		dns.RcodeServerFailure:  StatusServFail,
		dns.RcodeRefused:        StatusRefused,
		dns.RcodeFormatError:    StatusFormErr,
		dns.RcodeNotImplemented: StatusNotImp,
		dns.RcodeYXDomain:       Status("YXDOMAIN"),
	} {
		srv := testdns.Start(t, testdns.Reply(rcode))
		o := opts()
		o.Retries = 2
		res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeA, o)
		if res.Status != want {
			t.Errorf("rcode %d: status = %s, want %s", rcode, res.Status, want)
		}
		// Deterministic answers, including SERVFAIL, are never retried.
		if res.Attempts != 1 || srv.Queries.Load() != 1 {
			t.Errorf("rcode %d: attempts = %d, queries = %d, want 1", rcode, res.Attempts, srv.Queries.Load())
		}
		if want.Usable() != (res.Error == "") {
			t.Errorf("rcode %d: error = %q", rcode, res.Error)
		}
	}
}

func TestQueryNODATA(t *testing.T) {
	srv := testdns.Start(t, testdns.Reply(dns.RcodeSuccess))
	res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeA, opts())
	if res.Status != StatusNoError || len(res.Records) != 0 {
		t.Fatalf("got %s %v", res.Status, res.Records)
	}
}

func TestQueryTimeoutAndRetries(t *testing.T) {
	srv := testdns.Start(t, testdns.Silent())
	o := opts()
	o.Timeout = 100 * time.Millisecond
	o.Retries = 2
	o.RetryDelay = 20 * time.Millisecond
	start := time.Now()
	res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeA, o)
	elapsed := time.Since(start)
	if res.Status != StatusTimeout || res.Attempts != 3 || res.Error == "" {
		t.Fatalf("got %s attempts=%d err=%q", res.Status, res.Attempts, res.Error)
	}
	if n := srv.Queries.Load(); n != 3 {
		t.Fatalf("server saw %d queries, want 3", n)
	}
	if elapsed < 340*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("elapsed %v outside expected bounds", elapsed)
	}
	if res.Duration < 340*time.Millisecond {
		t.Fatalf("duration %v does not include all attempts", res.Duration)
	}
}

func TestQueryRetrySucceeds(t *testing.T) {
	var n atomic.Int64
	ok := testdns.Reply(dns.RcodeSuccess, "example.com. 60 IN A 10.0.0.1")
	srv := testdns.Start(t, func(req *dns.Msg, proto string) *dns.Msg {
		if n.Add(1) == 1 {
			return nil // first attempt times out
		}
		return ok(req, proto)
	})
	o := opts()
	o.Timeout = 100 * time.Millisecond
	o.Retries = 3
	res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeA, o)
	if res.Status != StatusNoError || res.Attempts != 2 || res.Error != "" {
		t.Fatalf("got %s attempts=%d err=%q", res.Status, res.Attempts, res.Error)
	}
}

func TestQueryNetworkError(t *testing.T) {
	o := opts()
	o.Protocol = TCP
	o.Retries = 1
	res := Query(context.Background(), resolver(t, testdns.ClosedPort(t)), "example.com", dns.TypeA, o)
	if res.Status != StatusNetworkError || res.Attempts != 2 {
		t.Fatalf("got %s attempts=%d err=%q", res.Status, res.Attempts, res.Error)
	}
}

func TestQueryTCP(t *testing.T) {
	srv := testdns.Start(t, func(req *dns.Msg, proto string) *dns.Msg {
		if proto != "tcp" {
			return nil
		}
		return testdns.Reply(dns.RcodeSuccess, "example.com. 60 IN A 10.0.0.1")(req, proto)
	})
	o := opts()
	o.Protocol = TCP
	res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeA, o)
	if res.Status != StatusNoError || res.ProtocolInitial != TCP || res.ProtocolFinal != TCP {
		t.Fatalf("got %+v", res)
	}
}

func TestQueryTCPFallback(t *testing.T) {
	srv := testdns.Start(t, testdns.TruncateUDP(testdns.Reply(dns.RcodeSuccess,
		"example.com. 60 IN A 10.0.0.1", "example.com. 60 IN A 10.0.0.2")))
	res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeA, opts())
	if res.Status != StatusNoError || res.ProtocolInitial != UDP || res.ProtocolFinal != TCP {
		t.Fatalf("got %s %s->%s", res.Status, res.ProtocolInitial, res.ProtocolFinal)
	}
	if res.Flags.Truncated || len(res.Records) != 2 || res.Attempts != 1 {
		t.Fatalf("got flags=%+v records=%v attempts=%d", res.Flags, res.Records, res.Attempts)
	}
}

func TestQueryNoTCPFallback(t *testing.T) {
	srv := testdns.Start(t, testdns.TruncateUDP(testdns.Reply(dns.RcodeSuccess, "example.com. 60 IN A 10.0.0.1")))
	o := opts()
	o.TCPFallback = false
	res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeA, o)
	if res.Status != StatusNoError || res.ProtocolFinal != UDP || !res.Flags.Truncated || len(res.Records) != 0 {
		t.Fatalf("got %+v", res)
	}
	if srv.Queries.Load() != 1 {
		t.Fatalf("queries = %d", srv.Queries.Load())
	}
}

func TestQueryCNAMEChain(t *testing.T) {
	srv := testdns.Start(t, testdns.Reply(dns.RcodeSuccess,
		"edge.example.net. 60 IN A 10.20.30.40", // out of order on purpose
		"www.example.com. 300 IN CNAME Frontend.example.net.",
		"frontend.example.net. 300 IN CNAME edge.example.net.",
		"unrelated.example.org. 60 IN A 192.0.2.1",
	))
	res := Query(context.Background(), resolver(t, srv.Addr), "WWW.example.com", dns.TypeA, opts())
	if !slices.Equal(res.CNAMEChain, []string{"frontend.example.net.", "edge.example.net."}) {
		t.Fatalf("chain = %q", res.CNAMEChain)
	}
	if res.FinalName != "edge.example.net." || !slices.Equal(values(res), []string{"10.20.30.40"}) || res.IgnoredRecords != 1 {
		t.Fatalf("final = %q records = %q", res.FinalName, values(res))
	}
}

func TestQueryCNAMELoopAndNXDOMAIN(t *testing.T) {
	srv := testdns.Start(t, testdns.Reply(dns.RcodeNameError,
		"a.example.com. 60 IN CNAME b.example.com.",
		"b.example.com. 60 IN CNAME a.example.com.",
	))
	res := Query(context.Background(), resolver(t, srv.Addr), "a.example.com", dns.TypeA, opts())
	if res.Status != StatusNXDomain || !slices.Equal(res.CNAMEChain, []string{"b.example.com."}) || len(res.Records) != 0 {
		t.Fatalf("got %s chain=%q records=%v", res.Status, res.CNAMEChain, res.Records)
	}
}

func TestQueryProtocolErrors(t *testing.T) {
	cases := map[string]testdns.Handler{
		"not a response": func(req *dns.Msg, _ string) *dns.Msg {
			m := testdns.Reply(dns.RcodeSuccess)(req, "")
			m.Response = false
			return m
		},
		"question mismatch": func(req *dns.Msg, _ string) *dns.Msg {
			m := testdns.Reply(dns.RcodeSuccess)(req, "")
			m.Question[0].Name = "other.example."
			return m
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := testdns.Start(t, h)
			res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeA, opts())
			if res.Status != StatusProtocolError || res.Attempts != 1 {
				t.Fatalf("got %s attempts=%d (%s)", res.Status, res.Attempts, res.Error)
			}
		})
	}
}

// A raw UDP server answering with garbage, and one sending a wrong ID first.
func TestQueryRawReplies(t *testing.T) {
	start := func(t *testing.T, reply func(req []byte) [][]byte) string {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pc.Close() })
		go func() {
			buf := make([]byte, 4096)
			for {
				n, addr, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				for _, b := range reply(buf[:n]) {
					_, _ = pc.WriteTo(b, addr)
				}
			}
		}()
		return pc.LocalAddr().String()
	}

	t.Run("garbage", func(t *testing.T) {
		addr := start(t, func(req []byte) [][]byte {
			return [][]byte{append(req[:2:2], 0x81, 0x80, 0, 1, 0, 5, 0, 0, 0, 0, 0xff)}
		})
		res := Query(context.Background(), resolver(t, addr), "example.com", dns.TypeA, opts())
		if res.Status != StatusProtocolError {
			t.Fatalf("got %s (%s)", res.Status, res.Error)
		}
	})
	t.Run("short", func(t *testing.T) {
		addr := start(t, func([]byte) [][]byte { return [][]byte{{1, 2, 3}} })
		res := Query(context.Background(), resolver(t, addr), "example.com", dns.TypeA, opts())
		if res.Status != StatusProtocolError {
			t.Fatalf("got %s (%s)", res.Status, res.Error)
		}
	})
	t.Run("wrong ID is ignored", func(t *testing.T) {
		addr := start(t, func(req []byte) [][]byte {
			q := new(dns.Msg)
			if err := q.Unpack(req); err != nil {
				return nil
			}
			good := testdns.Reply(dns.RcodeSuccess, "example.com. 1 IN A 10.0.0.9")(q, "udp")
			bad := good.Copy()
			bad.Id++
			bad.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 1}, A: net.IPv4(6, 6, 6, 6)}}
			b1, _ := bad.Pack()
			b2, _ := good.Pack()
			return [][]byte{b1, b2}
		})
		res := Query(context.Background(), resolver(t, addr), "example.com", dns.TypeA, opts())
		if res.Status != StatusNoError || !slices.Equal(values(res), []string{"10.0.0.9"}) {
			t.Fatalf("got %s %q", res.Status, values(res))
		}
	})
}

func TestQueryAllOrderAndBoundedConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int64
	reply := testdns.Reply(dns.RcodeSuccess, "example.com. 60 IN A 10.0.0.1")
	srv := testdns.Start(t, func(req *dns.Msg, proto string) *dns.Msg {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		return reply(req, proto)
	})
	// Many distinct resolver entries pointing at the same server; order is
	// tracked through names.
	var resolvers []Resolver
	for i := range 40 {
		r := resolver(t, srv.Addr)
		r.Name = string(rune('A' + i))
		resolvers = append(resolvers, r)
	}
	o := opts()
	o.Concurrency = 4
	results := QueryAll(context.Background(), resolvers, "example.com", dns.TypeA, o)
	for i, res := range results {
		if res.Resolver.Name != resolvers[i].Name || res.Status != StatusNoError {
			t.Fatalf("result %d: %s %s", i, res.Resolver.Name, res.Status)
		}
	}
	if p := peak.Load(); p > 4 || p < 2 {
		t.Fatalf("peak concurrency = %d, want 2..4", p)
	}
}

func TestQueryAllCancellation(t *testing.T) {
	srv := testdns.Start(t, testdns.Silent())
	var resolvers []Resolver
	for range 20 {
		resolvers = append(resolvers, resolver(t, srv.Addr))
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	o := opts()
	o.Timeout = 10 * time.Second
	o.Retries = 5
	o.Concurrency = 2
	start := time.Now()
	results := QueryAll(ctx, resolvers, "example.com", dns.TypeA, o)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	for _, res := range results {
		if res.Status != StatusNetworkError || res.Error != "query canceled" {
			t.Fatalf("got %s %q", res.Status, res.Error)
		}
	}
}

func TestQueryIPv6Resolver(t *testing.T) {
	srv := testdns.StartOn(t, "::1", testdns.Reply(dns.RcodeSuccess, "example.com. 60 IN AAAA 2001:db8::1"))
	res := Query(context.Background(), resolver(t, srv.Addr), "example.com", dns.TypeAAAA, opts())
	if res.Status != StatusNoError || !slices.Equal(values(res), []string{"2001:db8::1"}) {
		t.Fatalf("got %s %q (%s)", res.Status, values(res), res.Error)
	}
}

// 100 resolvers that each take 50ms must finish in about two rounds with
// concurrency 50, not in 100 sequential rounds.
func TestQueryAll100Resolvers(t *testing.T) {
	reply := testdns.Reply(dns.RcodeSuccess, "example.com. 60 IN A 10.0.0.1")
	srv := testdns.Start(t, func(req *dns.Msg, proto string) *dns.Msg {
		time.Sleep(50 * time.Millisecond)
		return reply(req, proto)
	})
	resolvers := make([]Resolver, 100)
	for i := range resolvers {
		resolvers[i] = resolver(t, srv.Addr)
	}
	o := opts()
	o.Concurrency = 50
	start := time.Now()
	results := QueryAll(context.Background(), resolvers, "example.com", dns.TypeA, o)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("100 resolvers took %v", elapsed)
	}
	for i, r := range results {
		if r.Status != StatusNoError {
			t.Fatalf("result %d: %s %s", i, r.Status, r.Error)
		}
	}
}
