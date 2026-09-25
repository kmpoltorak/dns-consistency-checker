package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"go.yaml.in/yaml/v3"

	"github.com/kmpoltorak/dns-consistency-checker/internal/compare"
	"github.com/kmpoltorak/dns-consistency-checker/internal/output"
	"github.com/kmpoltorak/dns-consistency-checker/internal/testdns"
)

// run executes the CLI in-process with an empty environment unless env is given.
func run(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Run(context.Background(), args, &out, &errb, func(k string) string { return env[k] })
	return code, out.String(), errb.String()
}

func checkJSON(t *testing.T, args ...string) (int, output.Document) {
	t.Helper()
	code, out, stderr := run(t, nil, append([]string{"check", "--output", "json", "--timeout", "300ms"}, args...)...)
	var doc output.Document
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\nstdout: %s\nstderr: %s", err, out, stderr)
	}
	return code, doc
}

func servers(addrs ...string) []string {
	var args []string
	for _, a := range addrs {
		args = append(args, "--server", a)
	}
	return args
}

func aRecords(ips ...string) testdns.Handler {
	var rrs []string
	for _, ip := range ips {
		rrs = append(rrs, "example.com. 300 IN A "+ip)
	}
	return testdns.Reply(dns.RcodeSuccess, rrs...)
}

func TestIntegrationScenario1SameA(t *testing.T) {
	a := testdns.Start(t, aRecords("10.0.0.1", "10.0.0.2"))
	b := testdns.Start(t, aRecords("10.0.0.1", "10.0.0.2"))
	c := testdns.Start(t, aRecords("10.0.0.1", "10.0.0.2"))
	code, doc := checkJSON(t, append([]string{"--host", "example.com"}, servers(a.Addr, b.Addr, c.Addr)...)...)
	if code != ExitConsistent || doc.Summary.Status != "CONSISTENT" || doc.Summary.Successful != 3 {
		t.Fatalf("code=%d summary=%+v", code, doc.Summary)
	}
	if len(doc.Groups) != 1 || !slices.Equal(doc.Groups[0].Answers, []string{"10.0.0.1", "10.0.0.2"}) || doc.Summary.MajorityGroup != 1 {
		t.Fatalf("groups=%+v", doc.Groups)
	}
}

func TestIntegrationScenario2DifferentOrder(t *testing.T) {
	a := testdns.Start(t, aRecords("10.0.0.1", "10.0.0.2"))
	b := testdns.Start(t, aRecords("10.0.0.2", "10.0.0.1"))
	code, doc := checkJSON(t, append([]string{"--host", "example.com"}, servers(a.Addr, b.Addr)...)...)
	if code != ExitConsistent || doc.Summary.Status != "CONSISTENT" {
		t.Fatalf("code=%d summary=%+v", code, doc.Summary)
	}
}

func TestIntegrationScenario3DifferentRRset(t *testing.T) {
	a := testdns.Start(t, aRecords("10.0.0.1", "10.0.0.2"))
	b := testdns.Start(t, aRecords("10.0.0.1"))
	c := testdns.Start(t, aRecords("10.0.0.1", "10.0.0.2"))
	code, doc := checkJSON(t, append([]string{"--host", "example.com"}, servers(a.Addr, b.Addr, c.Addr)...)...)
	if code != ExitInconsistent || doc.Summary.Status != "INCONSISTENT" || len(doc.Groups) != 2 {
		t.Fatalf("code=%d summary=%+v", code, doc.Summary)
	}
	if !slices.Equal(doc.Summary.Outliers, []string{b.Addr}) || doc.Issues[0].Type != "different_rrset" {
		t.Fatalf("outliers=%v issues=%+v", doc.Summary.Outliers, doc.Issues)
	}
}

func TestIntegrationScenario4Timeout(t *testing.T) {
	var addrs []string
	for range 3 {
		addrs = append(addrs, testdns.Start(t, aRecords("10.0.0.1")).Addr)
	}
	silent := testdns.Start(t, testdns.Silent())
	addrs = append(addrs, silent.Addr)
	code, doc := checkJSON(t, append([]string{"--host", "example.com", "--retries", "1", "--retry-delay", "10ms"}, servers(addrs...)...)...)
	if code != ExitPartialFailure || doc.Summary.Status != "PARTIAL_FAILURE" || doc.Summary.Failed != 1 {
		t.Fatalf("code=%d summary=%+v", code, doc.Summary)
	}
	r := doc.Results[3]
	if r.Status != "TIMEOUT" || r.Attempts != 2 || r.Error == nil || r.Error.Category != "timeout" || r.Group != 0 {
		t.Fatalf("result=%+v", r)
	}
	if silent.Queries.Load() != 2 {
		t.Fatalf("silent server saw %d queries", silent.Queries.Load())
	}
}

func TestIntegrationScenario5NXDOMAINMismatch(t *testing.T) {
	a := testdns.Start(t, aRecords("10.0.0.1"))
	b := testdns.Start(t, testdns.Reply(dns.RcodeNameError))
	code, doc := checkJSON(t, append([]string{"--host", "example.com"}, servers(a.Addr, b.Addr)...)...)
	if code != ExitInconsistent || doc.Summary.Status != "INCONSISTENT" {
		t.Fatalf("code=%d summary=%+v", code, doc.Summary)
	}
}

func TestIntegrationScenario6AllNXDOMAIN(t *testing.T) {
	a := testdns.Start(t, testdns.Reply(dns.RcodeNameError))
	b := testdns.Start(t, testdns.Reply(dns.RcodeNameError))
	code, doc := checkJSON(t, append([]string{"--host", "nope.example.com"}, servers(a.Addr, b.Addr)...)...)
	if code != ExitConsistent || doc.Groups[0].Status != "NXDOMAIN" {
		t.Fatalf("code=%d groups=%+v", code, doc.Groups)
	}
}

func TestIntegrationScenario7TTL(t *testing.T) {
	a := testdns.Start(t, testdns.Reply(dns.RcodeSuccess, "example.com. 300 IN A 10.0.0.1"))
	b := testdns.Start(t, testdns.Reply(dns.RcodeSuccess, "example.com. 120 IN A 10.0.0.1"))
	args := append([]string{"--host", "example.com"}, servers(a.Addr, b.Addr)...)

	code, doc := checkJSON(t, args...)
	if code != ExitConsistent || len(doc.Issues) != 1 || doc.Issues[0].Type != "ttl_difference" || doc.Issues[0].Severity != "info" {
		t.Fatalf("default: code=%d issues=%+v", code, doc.Issues)
	}
	if doc.Results[0].Answers[0].TTL != 300 || doc.Results[1].Answers[0].TTL != 120 {
		t.Fatalf("TTLs not collected: %+v", doc.Results)
	}

	code, doc = checkJSON(t, append(args, "--compare-ttl")...)
	if code != ExitInconsistent || doc.Summary.Status != "INCONSISTENT" || !doc.Query.CompareTTL {
		t.Fatalf("--compare-ttl: code=%d summary=%+v", code, doc.Summary)
	}
}

func TestIntegrationScenario8TCPFallback(t *testing.T) {
	srv := testdns.Start(t, testdns.TruncateUDP(aRecords("10.0.0.1")))
	code, doc := checkJSON(t, "--host", "example.com", "--server", srv.Addr)
	r := doc.Results[0]
	if code != ExitConsistent || r.Status != "NOERROR" || r.ProtocolInitial != "udp" || r.ProtocolFinal != "tcp" || len(r.Answers) != 1 {
		t.Fatalf("code=%d result=%+v", code, r)
	}
	if len(doc.Issues) != 1 || doc.Issues[0].Type != "tcp_fallback" || doc.Issues[0].Severity != "info" {
		t.Fatalf("issues=%+v", doc.Issues)
	}

	code, doc = checkJSON(t, "--host", "example.com", "--server", srv.Addr, "--no-tcp-fallback")
	r = doc.Results[0]
	if code != ExitConsistent || r.ProtocolFinal != "udp" || !r.Flags.Truncated || doc.Issues[0].Type != "truncated" {
		t.Fatalf("no fallback: code=%d result=%+v issues=%+v", code, r, doc.Issues)
	}
}

func TestIntegrationTCPProtocol(t *testing.T) {
	srv := testdns.Start(t, func(req *dns.Msg, proto string) *dns.Msg {
		if proto != "tcp" {
			return nil
		}
		return aRecords("10.0.0.1")(req, proto)
	})
	code, doc := checkJSON(t, "--host", "example.com", "--server", srv.Addr, "--protocol", "tcp")
	if code != ExitConsistent || doc.Results[0].ProtocolInitial != "tcp" || doc.Query.Protocol != "tcp" {
		t.Fatalf("code=%d doc=%+v", code, doc.Results[0])
	}
}

func TestIntegrationIPv6AndCustomPort(t *testing.T) {
	v4 := testdns.Start(t, aRecords("10.0.0.1"))
	v6 := testdns.StartOn(t, "::1", aRecords("10.0.0.1"))
	code, doc := checkJSON(t, "--host", "example.com", "--server", "v4="+v4.Addr, "--server", "v6="+v6.Addr)
	if code != ExitConsistent {
		t.Fatalf("code=%d doc=%+v", code, doc)
	}
	if doc.Results[1].Resolver.Name != "v6" || !strings.HasPrefix(doc.Results[1].Resolver.Address, "[::1]:") {
		t.Fatalf("resolver=%+v", doc.Results[1].Resolver)
	}
}

func TestIntegrationFailureStatuses(t *testing.T) {
	ok := testdns.Start(t, aRecords("10.0.0.1"))
	sf := testdns.Start(t, testdns.Reply(dns.RcodeServerFailure))
	ref := testdns.Start(t, testdns.Reply(dns.RcodeRefused))
	fe := testdns.Start(t, testdns.Reply(dns.RcodeFormatError))
	closed := testdns.ClosedPort(t)
	code, doc := checkJSON(t, append([]string{"--host", "example.com", "--protocol", "tcp", "--retries", "0"},
		servers(ok.Addr, sf.Addr, ref.Addr, fe.Addr, closed)...)...)
	if code != ExitPartialFailure {
		t.Fatalf("code=%d", code)
	}
	var got []string
	for _, r := range doc.Results {
		got = append(got, r.Status)
	}
	if want := []string{"NOERROR", "SERVFAIL", "REFUSED", "FORMERR", "NETWORK_ERROR"}; !slices.Equal(got, want) {
		t.Fatalf("statuses=%v", got)
	}

	code, _ = checkJSON(t, append([]string{"--host", "example.com"}, servers(sf.Addr, ref.Addr)...)...)
	if code != ExitTotalFailure {
		t.Fatalf("total failure code=%d", code)
	}
}

func TestIntegrationRecordTypesAndCNAME(t *testing.T) {
	chain := testdns.Reply(dns.RcodeSuccess,
		"www.example.com. 300 IN CNAME frontend.example.net.",
		"frontend.example.net. 300 IN CNAME edge.example.net.",
		"edge.example.net. 60 IN A 10.20.30.40")
	a, b := testdns.Start(t, chain), testdns.Start(t, chain)
	code, doc := checkJSON(t, "--host", "www.example.com", "--server", a.Addr, "--server", b.Addr)
	if code != ExitConsistent || !slices.Equal(doc.Groups[0].CNAMEChain, []string{"frontend.example.net.", "edge.example.net."}) ||
		doc.Results[0].FinalName != "edge.example.net." {
		t.Fatalf("code=%d group=%+v", code, doc.Groups[0])
	}

	mx1 := testdns.Start(t, testdns.Reply(dns.RcodeSuccess, "example.com. 1 IN MX 10 a.example.com.", "example.com. 1 IN MX 20 B.example.com."))
	mx2 := testdns.Start(t, testdns.Reply(dns.RcodeSuccess, "example.com. 1 IN MX 20 b.example.com.", "example.com. 1 IN MX 10 A.EXAMPLE.COM."))
	code, doc = checkJSON(t, "--host", "example.com", "--type", "mx", "--server", mx1.Addr, "--server", mx2.Addr,
		"--expected", "10 a.example.com", "--expected", "20 b.example.com.")
	if code != ExitConsistent || doc.Expected == nil || doc.Expected.Matching != 2 {
		t.Fatalf("MX: code=%d expected=%+v", code, doc.Expected)
	}

	txt := testdns.Start(t, testdns.Reply(dns.RcodeSuccess, `example.com. 1 IN TXT "a" "b"`))
	txt2 := testdns.Start(t, testdns.Reply(dns.RcodeSuccess, `example.com. 1 IN TXT "ab"`))
	code, _ = checkJSON(t, "--host", "example.com", "--type", "TXT", "--server", txt.Addr, "--server", txt2.Addr)
	if code != ExitInconsistent {
		t.Fatalf("TXT string boundaries: code=%d", code)
	}

	ptr := testdns.Start(t, func(req *dns.Msg, proto string) *dns.Msg {
		if req.Question[0].Name != "4.3.2.1.in-addr.arpa." {
			return testdns.Reply(dns.RcodeNameError)(req, proto)
		}
		return testdns.Reply(dns.RcodeSuccess, "4.3.2.1.in-addr.arpa. 1 IN PTR host.example.com.")(req, proto)
	})
	code, doc = checkJSON(t, "--host", "1.2.3.4", "--type", "PTR", "--server", ptr.Addr)
	if code != ExitConsistent || doc.Query.Name != "4.3.2.1.in-addr.arpa." || doc.Groups[0].Answers[0] != "host.example.com." {
		t.Fatalf("PTR: code=%d doc=%+v", code, doc.Groups)
	}
}

func TestIntegrationExpectedMode(t *testing.T) {
	good := testdns.Start(t, aRecords("10.20.30.41", "10.20.30.40"))
	partial := testdns.Start(t, aRecords("10.20.30.40"))
	failed := testdns.Start(t, testdns.Reply(dns.RcodeServerFailure))
	args := []string{"--host", "example.com", "--expected", "10.20.30.40", "--expected", "10.20.30.41"}

	code, doc := checkJSON(t, append(args, servers(good.Addr, failed.Addr)...)...)
	if code != ExitPartialFailure || doc.Expected.Matching != 1 || doc.Expected.Failed != 1 {
		t.Fatalf("code=%d expected=%+v", code, doc.Expected)
	}

	code, out, _ := run(t, nil, append(append([]string{"check", "--timeout", "300ms"}, args...), servers(good.Addr, partial.Addr, failed.Addr)...)...)
	if code != ExitInconsistent {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"Expected RRset:\n  10.20.30.40\n  10.20.30.41\n", "Matching resolvers: 1\n",
		"Non-matching resolvers: 1\n", "Failed resolvers: 1\n", "did not match the expected RRset"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestIntegrationTableOutput(t *testing.T) {
	a := testdns.Start(t, aRecords("10.20.30.40", "10.20.30.41"))
	b := testdns.Start(t, aRecords("10.20.30.40"))
	c := testdns.Start(t, aRecords("10.20.30.41", "10.20.30.40"))
	code, out, stderr := run(t, nil, "check", "--host", "example.com", "--server", "cloudflare="+a.Addr,
		"--server", "internal="+b.Addr, "--server", c.Addr)
	if code != ExitInconsistent || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{
		"DNS Consistency Check\n=====================\n",
		"Query:\nexample.com. A\n",
		"Resolvers checked: 3\n",
		"NAME         ADDRESS",
		"cloudflare   " + a.Addr,
		"Consistency:\nINCONSISTENT\n",
		"Majority response (2 of 3 resolvers; not necessarily correct):\n  10.20.30.40\n  10.20.30.41\n",
		"Response groups:\n",
		"- internal (" + b.Addr + ") returned a different RRset than the majority\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Rows keep input order; the second record is on a continuation line.
	iCloud, iInternal, iThird := strings.Index(out, "cloudflare   "), strings.Index(out, "internal   "), strings.Index(out, "-            "+c.Addr)
	if iCloud < 0 || iInternal < iCloud || iThird < iInternal {
		t.Errorf("rows out of order:\n%s", out)
	}
	if strings.Contains(out, "correct response") {
		t.Error("majority must never be called correct")
	}
}

func TestIntegrationYAMLAndExport(t *testing.T) {
	srv := testdns.Start(t, aRecords("10.0.0.1"))
	dir := t.TempDir()
	jsonPath, yamlPath := filepath.Join(dir, "result.json"), filepath.Join(dir, "result.yml")

	code, out, stderr := run(t, nil, "check", "--host", "example.com", "--server", srv.Addr, "--output", "yaml", "--export", jsonPath)
	if code != ExitConsistent {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var ydoc output.Document
	if err := yaml.Unmarshal([]byte(out), &ydoc); err != nil || ydoc.Summary.Status != "CONSISTENT" || ydoc.SchemaVersion != 1 {
		t.Fatalf("yaml stdout: %v %+v", err, ydoc.Summary)
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var jdoc output.Document
	if err := json.Unmarshal(data, &jdoc); err != nil || jdoc.Results[0].Answers[0].Value != "10.0.0.1" {
		t.Fatalf("json export: %v", err)
	}

	// Existing destination: refused before querying, exit 6.
	before := srv.Queries.Load()
	code, _, stderr = run(t, nil, "check", "--host", "example.com", "--server", srv.Addr, "--export", jsonPath)
	if code != ExitExportError || !strings.Contains(stderr, "--overwrite") || srv.Queries.Load() != before {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	code, _, _ = run(t, nil, "check", "--host", "example.com", "--server", srv.Addr, "--export", jsonPath, "--overwrite")
	if code != ExitConsistent {
		t.Fatalf("overwrite code=%d", code)
	}

	code, _, _ = run(t, nil, "check", "--host", "example.com", "--server", srv.Addr, "--export", yamlPath)
	data, _ = os.ReadFile(yamlPath)
	if code != ExitConsistent || !strings.Contains(string(data), "schema_version: 1\n") {
		t.Fatalf("yaml export code=%d:\n%s", code, data)
	}

	code, _, _ = run(t, nil, "check", "--host", "example.com", "--server", srv.Addr, "--export", filepath.Join(dir, "r.txt"))
	if code != ExitExportError {
		t.Fatalf("bad extension code=%d", code)
	}
	code, _, _ = run(t, nil, "check", "--host", "example.com", "--server", srv.Addr, "--export", filepath.Join(dir, "r.txt"), "--export-format", "yaml")
	if code != ExitConsistent {
		t.Fatalf("explicit format code=%d", code)
	}
}

func TestIntegrationConfigAndEnv(t *testing.T) {
	srv := testdns.Start(t, aRecords("10.0.0.1"))
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	content := "servers:\n  - name: local\n    address: \"" + srv.Addr + "\"\ndefaults:\n  protocol: tcp\n  retries: 0\n"
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := Run(context.Background(), []string{"check", "--host", "example.com", "--config", cfg, "--output", "json"},
		&out, &bytes.Buffer{}, func(k string) string { return map[string]string{"DNS_PROTOCOL": "udp", "DNS_QUERY_TIMEOUT": "2s"}[k] })
	var doc output.Document
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if code != ExitConsistent || doc.Query.Protocol != "udp" || doc.Query.TimeoutMS != 2000 || doc.Query.Retries != 0 ||
		doc.Results[0].Resolver.Name != "local" {
		t.Fatalf("code=%d query=%+v", code, doc.Query)
	}

	code, _, _ = run(t, map[string]string{"DNS_RETRIES": "many"}, "check", "--host", "example.com", "--config", cfg)
	if code != ExitConfigError {
		t.Fatalf("bad env code=%d", code)
	}
}

func TestIntegrationVerboseGoesToStderr(t *testing.T) {
	srv := testdns.Start(t, testdns.TruncateUDP(aRecords("10.0.0.1")))
	code, out, stderr := run(t, nil, "check", "--host", "example.com", "--server", srv.Addr, "--output", "json", "--verbose")
	if code != ExitConsistent || !strings.Contains(stderr, "retrying over TCP") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("stdout polluted: %s", out)
	}
	_, out, _ = run(t, nil, "check", "--host", "example.com", "--server", srv.Addr, "-v")
	if !strings.Contains(out, "PROTO") || !strings.Contains(out, "udp->tcp") {
		t.Fatalf("verbose table:\n%s", out)
	}
}

func TestCommands(t *testing.T) {
	tests := []struct {
		args       []string
		code       int
		stdoutHas  string
		stderrHas  string
		emptyStdio bool
	}{
		{[]string{"version"}, 0, "dns-consistency-checker ", "", false},
		{[]string{"help"}, 0, "Usage:", "", false},
		{[]string{"--help"}, 0, "Usage:", "", false},
		{[]string{"help", "check"}, 0, "--servers-file", "", false},
		{[]string{"check", "--help"}, 0, "--compare-ttl", "", false},
		{[]string{"check", "-h"}, 0, "--expected-file", "", false},
		{nil, ExitInvalidInput, "", "Usage:", true},
		{[]string{"bogus"}, ExitInvalidInput, "", "unknown command", true},
		{[]string{"help", "bogus"}, ExitInvalidInput, "", "unknown command", true},
		{[]string{"check"}, ExitInvalidInput, "", "--host is required", true},
		{[]string{"check", "--host", "example.com"}, ExitInvalidInput, "", "no resolvers", true},
		{[]string{"check", "--host", "example.com", "--server", "1.1.1.1", "extra"}, ExitInvalidInput, "", "unexpected argument", true},
		{[]string{"check", "--bogus"}, ExitInvalidInput, "", "flag provided but not defined", true},
		{[]string{"check", "--host", "example.com", "--server", "1.1.1.1", "--type", "ANY"}, ExitInvalidInput, "", "unsupported record type", true},
		{[]string{"check", "--host", "example.com", "--server", "1.1.1.1", "--timeout", "2m"}, ExitInvalidInput, "", "out of range", true},
		{[]string{"check", "--host", "example.com", "--server", "1.1.1.1", "--concurrency", "0"}, ExitInvalidInput, "", "out of range", true},
		{[]string{"check", "--host", "example.com", "--config", "/nonexistent.yaml"}, ExitConfigError, "", "config file", true},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, out, stderr := run(t, nil, tt.args...)
			if code != tt.code || !strings.Contains(out, tt.stdoutHas) || !strings.Contains(stderr, tt.stderrHas) {
				t.Fatalf("code=%d\nstdout=%s\nstderr=%s", code, out, stderr)
			}
			if tt.emptyStdio && out != "" {
				t.Fatalf("stdout not empty on error: %s", out)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	for s, want := range map[string]int{"CONSISTENT": 0, "INCONSISTENT": 1, "PARTIAL_FAILURE": 2, "TOTAL_FAILURE": 3, "?": 7} {
		if got := ExitCode(compare.Overall(s)); got != want {
			t.Errorf("ExitCode(%s) = %d, want %d", s, got, want)
		}
	}
}

func TestIntegrationDuplicateResolverIsReported(t *testing.T) {
	srv := testdns.Start(t, aRecords("10.0.0.1"))
	host, port, _ := strings.Cut(srv.Addr, ":")
	code, out, stderr := run(t, nil, "check", "--host", "example.com", "--server", srv.Addr, "--server", host+":"+port)
	if code != ExitConsistent || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "Resolvers checked: 1\n") || !strings.Contains(out, "- [info] duplicate resolver "+srv.Addr) {
		t.Fatalf("output:\n%s", out)
	}
	_, doc := checkJSON(t, "--host", "example.com", "--server", srv.Addr, "--server", "dup="+srv.Addr)
	if len(doc.Issues) != 1 || doc.Issues[0].Type != "duplicate_resolver" || doc.Issues[0].Severity != "info" {
		t.Fatalf("issues=%+v", doc.Issues)
	}
}
