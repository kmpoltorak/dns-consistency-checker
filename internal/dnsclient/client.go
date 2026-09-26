// Package dnsclient sends DNS queries to resolvers over UDP or TCP with
// per-exchange timeouts, TCP fallback on truncation, bounded retries and a
// bounded worker pool, and turns every outcome into a Result.
package dnsclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/kmpoltorak/dns-consistency-checker/internal/normalize"
)

// Status is the outcome of querying one resolver: a DNS RCODE name or one of
// the transport-level categories TIMEOUT, NETWORK_ERROR and PROTOCOL_ERROR.
type Status string

// Statuses that are distinguished explicitly. Other RCODEs (for example
// YXDOMAIN or NOTAUTH) are reported by their standard name.
const (
	StatusNoError       Status = "NOERROR"
	StatusNXDomain      Status = "NXDOMAIN"
	StatusServFail      Status = "SERVFAIL"
	StatusRefused       Status = "REFUSED"
	StatusFormErr       Status = "FORMERR"
	StatusNotImp        Status = "NOTIMP"
	StatusTimeout       Status = "TIMEOUT"
	StatusNetworkError  Status = "NETWORK_ERROR"
	StatusProtocolError Status = "PROTOCOL_ERROR"
)

// Usable reports whether the status is a DNS answer that can be compared
// (NOERROR or NXDOMAIN). Every other status is an operational failure.
func (s Status) Usable() bool { return s == StatusNoError || s == StatusNXDomain }

// Protocols.
const (
	UDP = "udp"
	TCP = "tcp"
)

// EDNSBufferSize is the advertised EDNS(0) UDP payload size (DNS Flag Day 2020).
const EDNSBufferSize = 1232

// maxCNAMEHops bounds CNAME chain following within one answer section. A
// longer chain is a PROTOCOL_ERROR, never a silently truncated answer.
const maxCNAMEHops = 16

// Options controls how queries are sent.
type Options struct {
	Protocol    string // UDP or TCP
	TCPFallback bool   // retry truncated UDP answers over TCP
	Timeout     time.Duration
	Retries     int // additional attempts for TIMEOUT and NETWORK_ERROR
	RetryDelay  time.Duration
	Concurrency int
	Logger      *slog.Logger // diagnostics; nil discards them
}

// Flags are header flags of the final DNS response.
type Flags struct {
	Authoritative      bool
	Truncated          bool
	RecursionDesired   bool
	RecursionAvailable bool
	AuthenticatedData  bool // as reported by the resolver; not validated here
	CheckingDisabled   bool
}

// Result is the outcome of querying one resolver.
type Result struct {
	Resolver        Resolver
	QueryName       string
	QueryType       string
	ProtocolInitial string
	ProtocolFinal   string
	Status          Status
	CNAMEChain      []string           // canonical CNAME targets, in order
	CNAMETTLs       []uint32           // TTL of the CNAME record leading to each CNAMEChain entry
	FinalName       string             // last name of the chain, or the query name
	Records         []normalize.Record // final RRset: sorted, de-duplicated
	Flags           Flags
	Duration        time.Duration // all attempts including retry delays; excludes resolver hostname lookup
	Attempts        int
	Time            time.Time // start of the first attempt, UTC
	Error           string    // last error message; empty on success
	IgnoredRecords  int       // answer records outside the CNAME chain or of other types
}

// protocolError marks malformed or mismatching responses.
type protocolError struct{ msg string }

func (e *protocolError) Error() string { return e.msg }

// QueryAll queries every resolver concurrently with at most
// opts.Concurrency queries in flight. Results are in resolver order.
func QueryAll(ctx context.Context, resolvers []Resolver, qname string, qtype uint16, opts Options) []Result {
	results := make([]Result, len(resolvers))
	sem := make(chan struct{}, max(opts.Concurrency, 1))
	var wg sync.WaitGroup
	for i, r := range resolvers {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			results[i] = newResult(r, qname, qtype, opts)
			results[i].Status, results[i].Error = StatusNetworkError, "query canceled"
			continue
		}
		wg.Go(func() {
			defer func() { <-sem }()
			results[i] = Query(ctx, r, qname, qtype, opts)
		})
	}
	wg.Wait()
	return results
}

func newResult(r Resolver, qname string, qtype uint16, opts Options) Result {
	return Result{
		Resolver:        r,
		QueryName:       dns.Fqdn(qname),
		QueryType:       dns.TypeToString[qtype],
		ProtocolInitial: opts.Protocol,
		ProtocolFinal:   opts.Protocol,
		FinalName:       normalize.Name(qname),
		Time:            time.Now().UTC(),
	}
}

// Query queries one resolver, retrying TIMEOUT and NETWORK_ERROR outcomes up
// to opts.Retries times. Deterministic answers, including SERVFAIL (which
// resolvers cache, RFC 9520), are never retried.
func Query(ctx context.Context, r Resolver, qname string, qtype uint16, opts Options) Result {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log = log.With("resolver", r.String())
	res := newResult(r, qname, qtype, opts)

	if r.Host != "" {
		start := time.Now()
		resolved, err := r.resolveHost(ctx, opts.Timeout)
		if err != nil {
			res.Attempts, res.Status, res.Error = 1, StatusNetworkError, err.Error()
			if ctx.Err() != nil {
				res.Error = "query canceled"
			}
			res.Duration = time.Since(start)
			log.Debug("resolver hostname lookup failed", "error", err)
			return res
		}
		res.Resolver = resolved
		log.Debug("resolved resolver hostname", "address", resolved.Endpoint.Addr(), "lookup", time.Since(start))
	}

	// Duration measures the DNS query itself, not the hostname lookup.
	start := time.Now()

	for attempt := 1; ; attempt++ {
		res.Attempts = attempt
		res.attempt(ctx, qtype, opts, log)
		if ctx.Err() != nil && !res.Status.Usable() {
			res.Status, res.Error = StatusNetworkError, "query canceled"
			break
		}
		if (res.Status != StatusTimeout && res.Status != StatusNetworkError) || attempt > opts.Retries {
			break
		}
		log.Debug("retrying", "attempt", attempt, "status", res.Status, "error", res.Error, "delay", opts.RetryDelay)
		if !sleep(ctx, opts.RetryDelay) {
			res.Status, res.Error = StatusNetworkError, "query canceled"
			break
		}
	}
	res.Duration = time.Since(start)
	log.Debug("done", "status", res.Status, "attempts", res.Attempts, "duration", res.Duration, "protocol", res.ProtocolFinal)
	return res
}

// attempt performs one attempt (including a possible TCP fallback) and
// overwrites the outcome fields of res.
func (res *Result) attempt(ctx context.Context, qtype uint16, opts Options, log *slog.Logger) {
	m := new(dns.Msg)
	m.SetQuestion(res.QueryName, qtype)
	m.RecursionDesired = true
	m.AuthenticatedData = true // RFC 6840 section 5.7: ask for the AD bit
	m.SetEdns0(EDNSBufferSize, false)

	proto := opts.Protocol
	log.Debug("sending query", "attempt", res.Attempts, "protocol", proto)
	resp, err := exchange(ctx, proto, res.Resolver.Endpoint, m, opts.Timeout)
	if err == nil && resp.Truncated && proto == UDP && opts.TCPFallback {
		log.Debug("UDP response truncated, retrying over TCP", "attempt", res.Attempts)
		proto = TCP
		resp, err = exchange(ctx, proto, res.Resolver.Endpoint, m, opts.Timeout)
	}
	res.ProtocolFinal = proto
	res.CNAMEChain, res.CNAMETTLs, res.Records, res.Flags, res.IgnoredRecords = nil, nil, nil, Flags{}, 0
	res.FinalName = normalize.Name(res.QueryName)
	if err != nil {
		res.Status, res.Error = classify(err, opts.Timeout)
		log.Debug("query failed", "attempt", res.Attempts, "status", res.Status, "error", res.Error)
		return
	}
	res.Error = ""
	res.Flags = Flags{
		Authoritative:      resp.Authoritative,
		Truncated:          resp.Truncated,
		RecursionDesired:   resp.RecursionDesired,
		RecursionAvailable: resp.RecursionAvailable,
		AuthenticatedData:  resp.AuthenticatedData,
		CheckingDisabled:   resp.CheckingDisabled,
	}
	res.Status = rcodeStatus(resp.Rcode)
	if !res.Status.Usable() {
		res.Error = "resolver returned " + string(res.Status)
		return
	}
	if res.IgnoredRecords, err = res.extract(resp, qtype); err != nil {
		res.Status, res.Error = StatusProtocolError, err.Error()
	}
}

// extract follows the CNAME chain from the query name through the answer
// section and collects the RRset of the queried type at the final name. It
// returns the number of answer records that were not used.
func (res *Result) extract(resp *dns.Msg, qtype uint16) (ignored int, err error) {
	name := res.FinalName
	if qtype != dns.TypeCNAME {
		cnames := map[string]*dns.CNAME{}
		for _, rr := range resp.Answer {
			if c, ok := rr.(*dns.CNAME); ok {
				cnames[normalize.Name(c.Hdr.Name)] = c
			}
		}
		seen := map[string]bool{name: true}
		for hops := 0; ; hops++ {
			c, ok := cnames[name]
			if !ok || seen[normalize.Name(c.Target)] {
				break // end of chain or CNAME loop
			}
			if hops == maxCNAMEHops {
				res.CNAMEChain, res.CNAMETTLs = nil, nil
				return 0, &protocolError{fmt.Sprintf("CNAME chain longer than %d hops", maxCNAMEHops)}
			}
			name = normalize.Name(c.Target)
			seen[name] = true
			res.CNAMEChain = append(res.CNAMEChain, name)
			res.CNAMETTLs = append(res.CNAMETTLs, c.Hdr.Ttl)
		}
	}
	res.FinalName = name

	var records []normalize.Record
	for _, rr := range resp.Answer {
		rec, supported, err := normalize.FromRR(rr)
		if err != nil {
			return 0, &protocolError{fmt.Sprintf("malformed %s record: %v", dns.TypeToString[rr.Header().Rrtype], err)}
		}
		switch {
		case supported && rr.Header().Rrtype == qtype && rec.Name == name:
			records = append(records, rec)
		case rr.Header().Rrtype == dns.TypeCNAME && qtype != dns.TypeCNAME && res.onChain(rec.Name):
			// part of the chain
		default:
			ignored++
		}
	}
	res.Records = normalize.RRset(records)
	return ignored, nil
}

func (res *Result) onChain(owner string) bool {
	if owner == normalize.Name(res.QueryName) {
		return true
	}
	for _, n := range res.CNAMEChain {
		if n == owner {
			return true
		}
	}
	return false
}

// exchange sends one query over the given protocol and waits for the
// matching response. The exchange is bounded by timeout and aborted
// immediately when ctx is cancelled.
func exchange(ctx context.Context, proto string, endpoint netip.AddrPort, m *dns.Msg, timeout time.Duration) (*dns.Msg, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, proto, endpoint.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Cancellation or timeout sets an immediate deadline, unblocking I/O.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	m.Id = dns.Id()
	co := &dns.Conn{Conn: conn, UDPSize: dns.MaxMsgSize}
	if err := co.WriteMsg(m); err != nil {
		return nil, err
	}
	for {
		raw, err := co.ReadMsgHeader(nil)
		if err != nil {
			if errors.Is(err, dns.ErrShortRead) {
				return nil, &protocolError{"short DNS message"}
			}
			return nil, err
		}
		resp := new(dns.Msg)
		unpackErr := resp.Unpack(raw)
		if resp.Id != m.Id {
			if proto == UDP {
				continue // stray or spoofed datagram: keep waiting, as dig does
			}
			return nil, &protocolError{fmt.Sprintf("response ID %d does not match query ID %d", resp.Id, m.Id)}
		}
		if unpackErr != nil {
			if resp.Truncated {
				return resp, nil // a truncated message may be cut mid-record
			}
			return nil, &protocolError{"malformed DNS response: " + unpackErr.Error()}
		}
		return resp, validate(m, resp)
	}
}

// validate checks that resp answers query.
func validate(query, resp *dns.Msg) error {
	if !resp.Response {
		return &protocolError{"reply is not a DNS response (QR=0)"}
	}
	if resp.Opcode != query.Opcode {
		return &protocolError{"response opcode does not match query"}
	}
	// Some servers omit the question in error responses.
	if len(resp.Question) == 0 && resp.Rcode != dns.RcodeSuccess {
		return nil
	}
	q := query.Question[0]
	if len(resp.Question) != 1 || !strings.EqualFold(resp.Question[0].Name, q.Name) ||
		resp.Question[0].Qtype != q.Qtype || resp.Question[0].Qclass != q.Qclass {
		return &protocolError{"response question section does not match the query"}
	}
	return nil
}

// classify maps an exchange error to a status and a message.
func classify(err error, timeout time.Duration) (Status, string) {
	var perr *protocolError
	var nerr net.Error
	switch {
	case errors.As(err, &perr):
		return StatusProtocolError, perr.msg
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &nerr) && nerr.Timeout():
		return StatusTimeout, fmt.Sprintf("no response within %s", timeout)
	default:
		return StatusNetworkError, err.Error()
	}
}

func rcodeStatus(rcode int) Status {
	switch rcode {
	case dns.RcodeSuccess:
		return StatusNoError
	case dns.RcodeNameError:
		return StatusNXDomain
	case dns.RcodeServerFailure:
		return StatusServFail
	case dns.RcodeRefused:
		return StatusRefused
	case dns.RcodeFormatError:
		return StatusFormErr
	case dns.RcodeNotImplemented:
		return StatusNotImp
	}
	if s, ok := dns.RcodeToString[rcode]; ok {
		return Status(s)
	}
	return Status(fmt.Sprintf("RCODE%d", rcode))
}

// sleep waits for d or until ctx is done; it reports whether d elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
