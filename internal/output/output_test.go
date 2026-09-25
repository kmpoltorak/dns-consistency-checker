package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kmpoltorak/dns-consistency-checker/internal/compare"
	"github.com/kmpoltorak/dns-consistency-checker/internal/dnsclient"
	"github.com/kmpoltorak/dns-consistency-checker/internal/normalize"
)

func report(t *testing.T) *compare.Report {
	t.Helper()
	r1, _ := dnsclient.ParseResolver("cloudflare", "1.1.1.1")
	r2, _ := dnsclient.ParseResolver("", "[2001:db8::1]:5353")
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	results := []dnsclient.Result{
		{Resolver: r1, QueryName: "example.com.", QueryType: "A", ProtocolInitial: "udp", ProtocolFinal: "tcp",
			Status: dnsclient.StatusNoError, FinalName: "example.com.", Attempts: 1, Time: ts, Duration: 18 * time.Millisecond,
			Flags:   dnsclient.Flags{RecursionDesired: true, RecursionAvailable: true},
			Records: []normalize.Record{{Name: "example.com.", Type: "A", Value: "10.0.0.1", TTL: 300, Raw: "raw"}}},
		{Resolver: r2, QueryName: "example.com.", QueryType: "A", ProtocolInitial: "udp", ProtocolFinal: "udp",
			Status: dnsclient.StatusTimeout, FinalName: "example.com.", Attempts: 2, Time: ts, Duration: 3 * time.Second,
			Error: "no response within 3s"},
	}
	rep := compare.Analyze(results, compare.Options{Expected: []normalize.Record{{Value: "10.0.0.1"}}})
	return &rep
}

var meta = Meta{Version: "test", QueryName: "example.com.", QueryType: "A", Protocol: "udp", TCPFallback: true, Timeout: 3 * time.Second, Retries: 1}

func TestJSONSchema(t *testing.T) {
	b, err := Marshal(NewDocument(report(t), meta), "json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schema_version", "tool", "query", "summary", "expected", "groups", "results", "issues"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	s := string(b)
	for _, want := range []string{
		`"status": "PARTIAL_FAILURE"`, `"majority_group": 1`, `"outliers": []`, `"cname_chain": []`,
		`"address": "1.1.1.1:53"`, `"address": "[2001:db8::1]:5353"`, `"duration_ms": 18`, `"timestamp": "2026-01-02T03:04:05Z"`,
		`"category": "timeout"`, `"protocol_final": "tcp"`, `"recursion_available": true`, `"timeout_ms": 3000`,
		`"matching_resolvers": [` + "\n" + `      "cloudflare (1.1.1.1)"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %s", want)
		}
	}
	// Successful results have no error object.
	if strings.Count(s, `"error":`) != 1 {
		t.Errorf("expected exactly one error object:\n%s", s)
	}
}

func TestYAMLRoundTripMatchesJSON(t *testing.T) {
	doc := NewDocument(report(t), meta)
	y, err := Marshal(doc, "yaml")
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML Document
	if err := yaml.Unmarshal(y, &fromYAML); err != nil {
		t.Fatal(err)
	}
	j1, _ := json.Marshal(doc)
	j2, _ := json.Marshal(fromYAML)
	if !bytes.Equal(j1, j2) {
		t.Fatalf("YAML round trip differs:\n%s\n%s", j1, j2)
	}
	if !strings.Contains(string(y), "schema_version: 1\n") || !strings.Contains(string(y), "  status: PARTIAL_FAILURE\n") {
		t.Fatalf("unexpected YAML:\n%s", y)
	}
}

func TestMarshalDeterministic(t *testing.T) {
	rep := report(t)
	a, _ := Marshal(NewDocument(rep, meta), "json")
	b, _ := Marshal(NewDocument(rep, meta), "json")
	if !bytes.Equal(a, b) {
		t.Fatal("output not stable")
	}
	if _, err := Marshal(Document{}, "xml"); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestExport(t *testing.T) {
	rep := report(t)
	p := filepath.Join(t.TempDir(), "r.yaml")
	if err := Export(p, "yaml", false, rep, meta); err != nil {
		t.Fatal(err)
	}
	if err := Export(p, "yaml", false, rep, meta); !os.IsExist(err) {
		t.Fatalf("second export without overwrite: %v", err)
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 100000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Export(p, "json", true, rep, meta); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !json.Valid(data) {
		t.Fatal("overwrite did not truncate")
	}
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	m := meta
	m.Verbose = true
	if err := Render(&buf, "table", report(t), m); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Protocol:\nUDP (TCP fallback on truncation)\n",
		"NAME         ADDRESS              STATUS       RESPONSE   TIME   PROTO      ATTEMPTS   FLAGS\n",
		"cloudflare   1.1.1.1              ✅ NOERROR   10.0.0.1   18ms   udp->tcp   1          rd ra\n",
		"-            [2001:db8::1]:5353   🚫 TIMEOUT   -          3s     udp        2          -\n",
		"Consistency:\n⚠️ PARTIAL_FAILURE\n",
		"Matching resolvers: 1\n",
		"- [2001:db8::1]:5353 timed out: no response within 3s (2 attempts)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasSuffix(l, " ") {
			t.Errorf("trailing whitespace: %q", l)
		}
	}
}

func TestResponseLines(t *testing.T) {
	r := dnsclient.Result{Status: dnsclient.StatusNoError, CNAMEChain: []string{"a.example."},
		Records: []normalize.Record{{Value: "1"}, {Value: "2"}}}
	if got := strings.Join(responseLines(r), "|"); got != "CNAME a.example.|1|2" {
		t.Errorf("got %q", got)
	}
	if got := responseLines(dnsclient.Result{Status: dnsclient.StatusNoError}); got[0] != "(no records)" {
		t.Errorf("got %q", got)
	}
	if got := responseLines(dnsclient.Result{Status: dnsclient.StatusNXDomain}); got[0] != "-" {
		t.Errorf("got %q", got)
	}
}

func TestDisplayWidthAndAlignment(t *testing.T) {
	if displayWidth("✅ NOERROR") != 10 || displayWidth("NOERROR") != 7 || displayWidth("🚫") != 2 || displayWidth("ℹ️") != 2 {
		t.Fatal("displayWidth wrong")
	}
	lines, width := alignColumns([][]string{{"STATUS", "X"}, {"✅ NOERROR", "Y"}, {"", "Z"}})
	want := []string{"STATUS       X", "✅ NOERROR   Y", "             Z"}
	if strings.Join(lines, "|") != strings.Join(want, "|") || width != 14 {
		t.Fatalf("got %q width %d", lines, width)
	}
}
