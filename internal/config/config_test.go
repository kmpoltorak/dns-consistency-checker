package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func base() Input {
	return Input{Host: "example.com", Type: "A", Servers: []string{"1.1.1.1"}, Set: map[string]bool{}}
}

func kindOf(t *testing.T, err error) Kind {
	t.Helper()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *config.Error", err)
	}
	return ce.Kind
}

func TestDefaults(t *testing.T) {
	s, err := Resolve(base(), env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.Protocol != "udp" || !s.TCPFallback || s.Timeout != DefaultTimeout || s.Retries != DefaultRetries ||
		s.RetryDelay != DefaultRetryDelay || s.Concurrency != DefaultConcurrency || s.Output != "table" ||
		s.CompareTTL || s.Expected != nil || s.QueryName != "example.com." || s.Type != dns.TypeA {
		t.Fatalf("unexpected defaults: %+v", s)
	}
}

func TestPrecedence(t *testing.T) {
	cfg := write(t, "c.yaml", `
servers:
  - name: cfg
    address: 9.9.9.9
defaults:
  timeout: 5s
  retries: 3
  retry_delay: 1s
  concurrency: 7
  protocol: tcp
  tcp_fallback: false
  compare_ttl: true
`)
	e := env(map[string]string{EnvTimeout: "4s", EnvRetries: "2", EnvConcurrency: "20"})

	// Config file only.
	in := base()
	in.Servers = nil
	in.ConfigFile = cfg
	s, err := Resolve(in, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.Timeout != 5*time.Second || s.Retries != 3 || s.RetryDelay != time.Second || s.Concurrency != 7 ||
		s.Protocol != "tcp" || s.TCPFallback || !s.CompareTTL || len(s.Resolvers) != 1 || s.Resolvers[0].Name != "cfg" {
		t.Fatalf("config not applied: %+v", s)
	}

	// Environment overrides config.
	s, err = Resolve(in, e)
	if err != nil {
		t.Fatal(err)
	}
	if s.Timeout != 4*time.Second || s.Retries != 2 || s.Concurrency != 20 || s.RetryDelay != time.Second {
		t.Fatalf("env not applied: %+v", s)
	}

	// Flags override environment; only explicitly set flags count.
	in.Timeout, in.Retries, in.Concurrency = time.Second, 0, 99
	in.Set = map[string]bool{"timeout": true, "retries": true}
	in.Servers = []string{"1.1.1.1"}
	s, err = Resolve(in, e)
	if err != nil {
		t.Fatal(err)
	}
	if s.Timeout != time.Second || s.Retries != 0 || s.Concurrency != 20 {
		t.Fatalf("flags not applied: %+v", s)
	}
	// CLI resolvers replace the config file's list.
	if len(s.Resolvers) != 1 || s.Resolvers[0].Address() != "1.1.1.1" {
		t.Fatalf("resolvers = %v", s.Resolvers)
	}
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
		env    map[string]string
		kind   Kind
	}{
		{"no host", func(in *Input) { in.Host = " " }, nil, KindInput},
		{"bad host", func(in *Input) { in.Host = "exa mple.com" }, nil, KindInput},
		{"bad type", func(in *Input) { in.Type = "ANY" }, nil, KindInput},
		{"no resolvers", func(in *Input) { in.Servers = nil }, nil, KindInput},
		{"bad resolver", func(in *Input) { in.Servers = []string{"dns.google"} }, nil, KindInput},
		{"bad port", func(in *Input) { in.Servers = []string{"1.1.1.1:0"} }, nil, KindInput},
		{"timeout too big", func(in *Input) { in.Timeout = time.Hour; in.Set["timeout"] = true }, nil, KindInput},
		{"timeout zero", func(in *Input) { in.Timeout = 0; in.Set["timeout"] = true }, nil, KindInput},
		{"retries negative", func(in *Input) { in.Retries = -1; in.Set["retries"] = true }, nil, KindInput},
		{"retries too many", func(in *Input) { in.Retries = 11; in.Set["retries"] = true }, nil, KindInput},
		{"retry delay", func(in *Input) { in.RetryDelay = time.Minute; in.Set["retry-delay"] = true }, nil, KindInput},
		{"concurrency zero", func(in *Input) { in.Concurrency = 0; in.Set["concurrency"] = true }, nil, KindInput},
		{"concurrency big", func(in *Input) { in.Concurrency = 1001; in.Set["concurrency"] = true }, nil, KindInput},
		{"protocol", func(in *Input) { in.Protocol = "quic"; in.Set["protocol"] = true }, nil, KindInput},
		{"output", func(in *Input) { in.Output = "csv"; in.Set["output"] = true }, nil, KindInput},
		{"expected bad", func(in *Input) { in.Expected = []string{"nope"} }, nil, KindInput},
		{"export format without export", func(in *Input) { in.ExportFormat = "json" }, nil, KindInput},
		{"export ext", func(in *Input) { in.Export = filepath.Join(os.TempDir(), "x.txt") }, nil, KindExport},
		{"export dir missing", func(in *Input) { in.Export = "/nonexistent-dir-xyz/r.json" }, nil, KindExport},
		{"env timeout", nil, map[string]string{EnvTimeout: "soon"}, KindConfig},
		{"env retries", nil, map[string]string{EnvRetries: "x"}, KindConfig},
		{"env concurrency range", nil, map[string]string{EnvConcurrency: "0"}, KindConfig},
		{"env protocol", nil, map[string]string{EnvProtocol: "http"}, KindConfig},
		{"missing config", func(in *Input) { in.ConfigFile = "/nonexistent/config.yaml" }, nil, KindConfig},
		{"missing servers file", func(in *Input) { in.ServersFile = "/nonexistent/servers.txt" }, nil, KindInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			if tt.mutate != nil {
				tt.mutate(&in)
			}
			_, err := Resolve(in, env(tt.env))
			if err == nil {
				t.Fatal("expected error")
			}
			if k := kindOf(t, err); k != tt.kind {
				t.Fatalf("kind = %d, want %d (%v)", k, tt.kind, err)
			}
		})
	}
}

func TestConfigFileErrors(t *testing.T) {
	for name, content := range map[string]string{
		"unknown key":   "servers: []\nbogus: 1\n",
		"bad yaml":      "servers: [\n",
		"bad timeout":   "defaults:\n  timeout: forever\n",
		"bad retries":   "defaults:\n  retries: 99\n",
		"bad protocol":  "defaults:\n  protocol: sctp\n",
		"bad server":    "servers:\n  - address: not-an-ip\n",
		"unknown field": "servers:\n  - addr: 1.1.1.1\n",
	} {
		t.Run(name, func(t *testing.T) {
			in := base()
			in.Servers = nil
			in.ConfigFile = write(t, "c.yaml", content)
			_, err := Resolve(in, env(nil))
			if err == nil || kindOf(t, err) != KindConfig {
				t.Fatalf("err = %v, want config error", err)
			}
		})
	}
	// An empty config file is valid.
	in := base()
	in.ConfigFile = write(t, "empty.yaml", "")
	if _, err := Resolve(in, env(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestServersFile(t *testing.T) {
	p := write(t, "resolvers.txt", `
# Public DNS

  1.1.1.1
cloudflare-v6 [2606:4700:4700::1111]:53
	8.8.8.8:53

# Internal DNS
internal 10.10.10.53:5353
1.1.1.1:53
`)
	in := base()
	in.Servers = []string{"google=8.8.8.8", "9.9.9.9"}
	in.ServersFile = p
	s, err := Resolve(in, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range s.Resolvers {
		got = append(got, r.String())
	}
	want := "google (8.8.8.8),9.9.9.9,1.1.1.1,cloudflare-v6 (2606:4700:4700::1111),internal (10.10.10.53:5353)"
	if strings.Join(got, ",") != want {
		t.Fatalf("resolvers = %s\nwant        %s", strings.Join(got, ","), want)
	}
	if len(s.Duplicates) != 2 {
		t.Fatalf("duplicates = %q", s.Duplicates)
	}

	bad := write(t, "bad.txt", "1.1.1.1 two three\n")
	in.ServersFile = bad
	if _, err := Resolve(in, env(nil)); err == nil || !strings.Contains(err.Error(), "bad.txt:1") {
		t.Fatalf("err = %v", err)
	}
}

func TestTooManyResolvers(t *testing.T) {
	var b strings.Builder
	for i := range MaxResolvers + 1 {
		fmt.Fprintf(&b, "10.0.%d.%d\n", i/256, i%256)
	}
	in := base()
	in.Servers = nil
	in.ServersFile = write(t, "many.txt", b.String())
	if _, err := Resolve(in, env(nil)); err == nil || !strings.Contains(err.Error(), "too many resolvers") {
		t.Fatalf("err = %v", err)
	}
}

func TestFileSizeLimit(t *testing.T) {
	in := base()
	in.ServersFile = write(t, "huge.txt", strings.Repeat("#", MaxFileSize+1))
	if _, err := Resolve(in, env(nil)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"example.com", "example.com.", "_ldap._tcp.example.com", "4.3.2.1.in-addr.arpa.",
		"xn--bcher-kva.example", "a-b.example", ".", "*.example.com", "localhost"} {
		if _, err := ValidateName(ok); err != nil {
			t.Errorf("ValidateName(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"a..b", "exa mple.com", "tab\t.com", strings.Repeat("a", 64) + ".com",
		strings.Repeat("abcdefghi.", 26) + "com"} {
		if _, err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) accepted", bad)
		}
	}
}

func TestPTRConversion(t *testing.T) {
	for host, want := range map[string]string{
		"1.2.3.4":              "4.3.2.1.in-addr.arpa.",
		"4.3.2.1.in-addr.arpa": "4.3.2.1.in-addr.arpa.",
		"2001:db8::1":          "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.",
		"fe80::1%en0":          "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.e.f.ip6.arpa.",
		"::ffff:1.2.3.4":       "4.3.2.1.in-addr.arpa.",
	} {
		in := base()
		in.Host, in.Type = host, "ptr"
		s, err := Resolve(in, env(nil))
		if err != nil {
			t.Fatal(err)
		}
		if s.QueryName != want || s.Host != host {
			t.Errorf("PTR %q -> %q, want %q", host, s.QueryName, want)
		}
	}
	// Non-PTR types never convert.
	in := base()
	in.Host = "1.2.3.4"
	s, err := Resolve(in, env(nil))
	if err != nil || s.QueryName != "1.2.3.4." {
		t.Fatalf("got %v %v", s, err)
	}
}

func TestExpected(t *testing.T) {
	in := base()
	in.Type = "MX"
	in.Expected = []string{"10 mail.example.com"}
	in.ExpectedFile = write(t, "exp.yaml", "records:\n  - 20 backup.example.com.\n")
	s, err := Resolve(in, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Expected) != 2 || s.Expected[0].Value != "10 mail.example.com." || s.Expected[1].Value != "20 backup.example.com." {
		t.Fatalf("expected = %+v", s.Expected)
	}

	// Empty expected file: expect NOERROR with no records.
	in = base()
	in.ExpectedFile = write(t, "empty.yaml", "records: []\n")
	s, err = Resolve(in, env(nil))
	if err != nil || s.Expected == nil || len(s.Expected) != 0 {
		t.Fatalf("expected = %#v err = %v", s.Expected, err)
	}

	in.ExpectedFile = write(t, "bad.yaml", "record: [x]\n")
	if _, err := Resolve(in, env(nil)); err == nil || kindOf(t, err) != KindInput {
		t.Fatalf("err = %v", err)
	}
}

func TestExportFormatFor(t *testing.T) {
	for _, tt := range []struct{ path, explicit, want string }{
		{"r.json", "", "json"}, {"r.JSON", "", "json"}, {"r.yaml", "", "yaml"}, {"r.yml", "", "yaml"},
		{"r.txt", "json", "json"}, {"r.json", "YAML", "yaml"},
	} {
		if got, err := ExportFormatFor(tt.path, tt.explicit); err != nil || got != tt.want {
			t.Errorf("ExportFormatFor(%q, %q) = %q, %v", tt.path, tt.explicit, got, err)
		}
	}
	for _, tt := range [][2]string{{"r.txt", ""}, {"r", ""}, {"r.json", "xml"}} {
		if _, err := ExportFormatFor(tt[0], tt[1]); err == nil {
			t.Errorf("ExportFormatFor(%q, %q) accepted", tt[0], tt[1])
		}
	}
}

func TestExportExisting(t *testing.T) {
	p := write(t, "r.json", "{}")
	in := base()
	in.Export = p
	if _, err := Resolve(in, env(nil)); err == nil || kindOf(t, err) != KindExport {
		t.Fatalf("err = %v", err)
	}
	in.Overwrite = true
	s, err := Resolve(in, env(nil))
	if err != nil || s.ExportFormat != "json" {
		t.Fatalf("got %v %v", s, err)
	}
}

// The documented example files must stay valid.
func TestExampleFiles(t *testing.T) {
	in := base()
	in.Servers = nil
	in.Type = "MX"
	in.ConfigFile = "../../testdata/config.yaml"
	in.ExpectedFile = "../../testdata/expected-mx.yaml"
	s, err := Resolve(in, env(nil))
	if err != nil || len(s.Resolvers) != 3 || len(s.Expected) != 2 {
		t.Fatalf("config example: %v %+v", err, s)
	}
	in.ServersFile = "../../testdata/resolvers.txt"
	s, err = Resolve(in, env(nil))
	if err != nil || len(s.Resolvers) != 4 {
		t.Fatalf("servers example: %v", err)
	}
}
