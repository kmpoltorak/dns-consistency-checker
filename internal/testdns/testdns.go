// Package testdns runs deterministic in-process DNS servers on ephemeral
// localhost ports for tests. It never touches the Internet.
package testdns

import (
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Handler builds the reply to a request. proto is "udp" or "tcp". Returning
// nil sends nothing, which makes the client time out.
type Handler func(req *dns.Msg, proto string) *dns.Msg

// Server is a running test DNS server serving UDP and TCP on the same port.
type Server struct {
	Addr    string       // host:port
	Queries atomic.Int64 // number of requests received (UDP and TCP)
}

// Start starts a server on 127.0.0.1 and stops it when the test ends.
func Start(t testing.TB, h Handler) *Server { return StartOn(t, "127.0.0.1", h) }

// StartOn starts a server on the given IP. It skips the test if the address
// family is unavailable (for example IPv6 on some CI hosts).
func StartOn(t testing.TB, ip string, h Handler) *Server {
	t.Helper()
	s := &Server{}
	var pc net.PacketConn
	var ln net.Listener
	var err error
	// Bind UDP on an ephemeral port, then TCP on the same port; retry on collision.
	for range 20 {
		pc, err = net.ListenPacket("udp", net.JoinHostPort(ip, "0"))
		if err != nil {
			t.Skipf("cannot listen on %s: %v", ip, err)
		}
		port := pc.LocalAddr().(*net.UDPAddr).Port
		ln, err = net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err == nil {
			break
		}
		pc.Close()
	}
	if err != nil {
		t.Fatalf("cannot bind UDP and TCP on the same port: %v", err)
	}
	s.Addr = pc.LocalAddr().String()

	serve := func(proto string) dns.HandlerFunc {
		return func(w dns.ResponseWriter, req *dns.Msg) {
			s.Queries.Add(1)
			if resp := h(req, proto); resp != nil {
				_ = w.WriteMsg(resp)
			}
		}
	}
	servers := []*dns.Server{
		{PacketConn: pc, Handler: serve("udp")},
		{Listener: ln, Handler: serve("tcp")},
	}
	for _, srv := range servers {
		started := make(chan struct{})
		srv.NotifyStartedFunc = func() { close(started) }
		go func() { _ = srv.ActivateAndServe() }()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("test DNS server did not start")
		}
	}
	t.Cleanup(func() {
		for _, srv := range servers {
			_ = srv.Shutdown()
		}
	})
	return s
}

// Reply returns a handler that answers every query with rcode and the given
// records (zone-file syntax), in the given order.
func Reply(rcode int, records ...string) Handler {
	rrs := make([]dns.RR, len(records))
	for i, s := range records {
		rr, err := dns.NewRR(s)
		if err != nil {
			panic("testdns: bad record " + s + ": " + err.Error())
		}
		rrs[i] = rr
	}
	return func(req *dns.Msg, _ string) *dns.Msg {
		m := new(dns.Msg)
		m.SetRcode(req, rcode)
		m.RecursionAvailable = true
		m.Answer = rrs
		return m
	}
}

// Silent returns a handler that never replies.
func Silent() Handler { return func(*dns.Msg, string) *dns.Msg { return nil } }

// TruncateUDP wraps h: UDP replies carry TC=1 and no answers; TCP replies
// come from h unchanged.
func TruncateUDP(h Handler) Handler {
	return func(req *dns.Msg, proto string) *dns.Msg {
		m := h(req, proto)
		if m != nil && proto == "udp" {
			m.Truncated = true
			m.Answer = nil
		}
		return m
	}
}

// ClosedPort returns a localhost address with no listener, for network errors.
func ClosedPort(t testing.TB) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	return addr
}
