// Package config resolves and validates the settings of a check from CLI
// flags, environment variables, an optional YAML configuration file and
// built-in defaults, in that order of precedence.
package config

import (
	"bufio"
	"bytes"
	"cmp"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/miekg/dns"
	"go.yaml.in/yaml/v3"

	"github.com/kmpoltorak/dns-consistency-checker/internal/dnsclient"
	"github.com/kmpoltorak/dns-consistency-checker/internal/normalize"
)

// Defaults.
const (
	DefaultType        = "A"
	DefaultProtocol    = dnsclient.UDP
	DefaultTimeout     = 3 * time.Second
	DefaultRetries     = 1
	DefaultRetryDelay  = 250 * time.Millisecond
	DefaultConcurrency = 50
	DefaultOutput      = "table"
)

// Limits that protect against accidental resource exhaustion.
const (
	MaxResolvers   = 1000
	MaxTimeout     = 60 * time.Second
	MinTimeout     = time.Millisecond
	MaxRetries     = 10
	MaxRetryDelay  = 10 * time.Second
	MaxConcurrency = 1000
	MaxExpected    = 1000
	MaxFileSize    = 1 << 20
)

// Environment variables.
const (
	EnvTimeout     = "DNS_QUERY_TIMEOUT"
	EnvRetries     = "DNS_RETRIES"
	EnvRetryDelay  = "DNS_RETRY_DELAY"
	EnvConcurrency = "DNS_MAX_CONCURRENCY"
	EnvProtocol    = "DNS_PROTOCOL"
)

// defaultResolvers is the built-in resolver list, in servers-file format.
//
//go:embed default-resolvers.txt
var defaultResolvers []byte

// Note types reported as informational issues.
const (
	NoteDuplicate        = "duplicate_resolver"
	NoteDefaultResolvers = "default_resolvers"
	NoteDefaultConfig    = "default_config"
)

// Note is an informational finding about the input, shown in the report.
type Note struct {
	Type    string
	Message string
}

// DefaultConfigPath returns the per-user configuration file that is loaded
// automatically when --config is not given:
// $XDG_CONFIG_HOME/dns-consistency-checker/config.yaml, falling back to
// ~/.config/... on Unix and %AppData%\... on Windows. It returns "" when no
// base directory is known. getenv is os.Getenv outside tests.
func DefaultConfigPath(goos string, getenv func(string) string) string {
	base := getenv("XDG_CONFIG_HOME")
	switch {
	case base != "":
	case goos == "windows":
		base = getenv("AppData")
	case getenv("HOME") != "":
		base = filepath.Join(getenv("HOME"), ".config")
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, "dns-consistency-checker", "config.yaml")
}

// Kind classifies an error so the CLI can map it to an exit code.
type Kind int

// Error kinds.
const (
	KindInput  Kind = iota // command-line input, servers file, expected values
	KindConfig             // configuration file or environment variables
	KindExport             // export destination
)

// Error is a validation error of a given Kind.
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func errorf(kind Kind, format string, args ...any) error {
	return &Error{Kind: kind, Err: fmt.Errorf(format, args...)}
}

// Input holds the raw command-line values. Set records which flags were given
// explicitly (by flag name without dashes); only those override lower layers.
type Input struct {
	Host, Type  string
	Servers     []string // "ADDR" or "NAME=ADDR"
	ServersFile string
	ConfigFile  string
	// DefaultConfigFile is loaded like --config when ConfigFile is empty and
	// the file exists (see DefaultConfigPath). Empty disables it.
	DefaultConfigFile string
	Protocol          string
	Timeout           time.Duration
	Retries           int
	RetryDelay        time.Duration
	Concurrency       int
	CompareTTL        bool
	NoTCPFallback     bool
	Expected          []string
	ExpectedFile      string
	Output            string
	Export            string
	ExportFormat      string
	Overwrite         bool
	Verbose           bool
	Set               map[string]bool
}

// Settings is the fully resolved and validated configuration of a check.
type Settings struct {
	Host         string // as given by the user
	QueryName    string // fully qualified name that is queried
	Type         uint16
	Resolvers    []dnsclient.Resolver
	Notes        []Note // informational findings about the input
	Protocol     string
	TCPFallback  bool
	Timeout      time.Duration
	Retries      int
	RetryDelay   time.Duration
	Concurrency  int
	CompareTTL   bool
	Expected     []normalize.Record // nil unless expected mode is enabled
	Output       string
	Export       string
	ExportFormat string // "json" or "yaml" when Export is set
	Overwrite    bool
	Verbose      bool
}

// fileConfig is the YAML configuration file schema.
type fileConfig struct {
	Servers []struct {
		Name    string `yaml:"name"`
		Address string `yaml:"address"`
	} `yaml:"servers"`
	Defaults struct {
		Timeout     *string `yaml:"timeout"`
		Retries     *int    `yaml:"retries"`
		RetryDelay  *string `yaml:"retry_delay"`
		Concurrency *int    `yaml:"concurrency"`
		Protocol    *string `yaml:"protocol"`
		TCPFallback *bool   `yaml:"tcp_fallback"`
		CompareTTL  *bool   `yaml:"compare_ttl"`
	} `yaml:"defaults"`
}

// expectedFile is the --expected-file YAML schema.
type expectedFile struct {
	Records []string `yaml:"records"`
}

// Resolve builds Settings from in, the environment (getenv) and the optional
// configuration file, and validates everything. It performs no network I/O.
func Resolve(in Input, getenv func(string) string) (*Settings, error) {
	s := &Settings{
		Protocol:    DefaultProtocol,
		TCPFallback: true,
		Timeout:     DefaultTimeout,
		Retries:     DefaultRetries,
		RetryDelay:  DefaultRetryDelay,
		Concurrency: DefaultConcurrency,
		Output:      DefaultOutput,
		Overwrite:   in.Overwrite,
		Verbose:     in.Verbose,
	}

	// Layer 1: configuration file.
	var cfg fileConfig
	configFile := in.ConfigFile
	if configFile == "" && in.DefaultConfigFile != "" {
		if _, err := os.Stat(in.DefaultConfigFile); err == nil {
			configFile = in.DefaultConfigFile
			s.Notes = append(s.Notes, Note{NoteDefaultConfig, "loaded default configuration file " + configFile})
		}
	}
	if configFile != "" {
		if err := loadConfig(configFile, &cfg); err != nil {
			return nil, err
		}
		if err := s.applyConfig(&cfg); err != nil {
			return nil, err
		}
	}
	// Layer 2: environment.
	if err := s.applyEnv(getenv); err != nil {
		return nil, err
	}
	// Layer 3: explicit flags.
	if err := s.applyFlags(in); err != nil {
		return nil, err
	}

	if err := s.resolveQuery(in); err != nil {
		return nil, err
	}
	if err := s.resolveResolvers(in, &cfg); err != nil {
		return nil, err
	}
	if err := s.resolveExpected(in); err != nil {
		return nil, err
	}
	switch s.Output {
	case "table", "json", "yaml":
	default:
		return nil, errorf(KindInput, "invalid --output %q: must be table, json or yaml", s.Output)
	}
	if err := s.resolveExport(in); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Settings) applyConfig(cfg *fileConfig) error {
	d := cfg.Defaults
	wrap := func(err error) error {
		if err != nil {
			return errorf(KindConfig, "config file: %v", err)
		}
		return nil
	}
	if d.Timeout != nil {
		if err := wrap(setDuration("defaults.timeout", *d.Timeout, &s.Timeout, MinTimeout, MaxTimeout)); err != nil {
			return err
		}
	}
	if d.RetryDelay != nil {
		if err := wrap(setDuration("defaults.retry_delay", *d.RetryDelay, &s.RetryDelay, 0, MaxRetryDelay)); err != nil {
			return err
		}
	}
	if d.Retries != nil {
		if err := wrap(setInt("defaults.retries", *d.Retries, &s.Retries, 0, MaxRetries)); err != nil {
			return err
		}
	}
	if d.Concurrency != nil {
		if err := wrap(setInt("defaults.concurrency", *d.Concurrency, &s.Concurrency, 1, MaxConcurrency)); err != nil {
			return err
		}
	}
	if d.Protocol != nil {
		if err := wrap(s.setProtocol("defaults.protocol", *d.Protocol)); err != nil {
			return err
		}
	}
	if d.TCPFallback != nil {
		s.TCPFallback = *d.TCPFallback
	}
	if d.CompareTTL != nil {
		s.CompareTTL = *d.CompareTTL
	}
	return nil
}

func (s *Settings) applyEnv(getenv func(string) string) error {
	wrap := func(err error) error {
		if err != nil {
			return errorf(KindConfig, "environment: %v", err)
		}
		return nil
	}
	if v := getenv(EnvTimeout); v != "" {
		if err := wrap(setDuration(EnvTimeout, v, &s.Timeout, MinTimeout, MaxTimeout)); err != nil {
			return err
		}
	}
	if v := getenv(EnvRetryDelay); v != "" {
		if err := wrap(setDuration(EnvRetryDelay, v, &s.RetryDelay, 0, MaxRetryDelay)); err != nil {
			return err
		}
	}
	for _, e := range []struct {
		name     string
		dst      *int
		min, max int
	}{{EnvRetries, &s.Retries, 0, MaxRetries}, {EnvConcurrency, &s.Concurrency, 1, MaxConcurrency}} {
		v := getenv(e.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return errorf(KindConfig, "environment: %s=%q is not an integer", e.name, v)
		}
		if err := wrap(setInt(e.name, n, e.dst, e.min, e.max)); err != nil {
			return err
		}
	}
	if v := getenv(EnvProtocol); v != "" {
		return wrap(s.setProtocol(EnvProtocol, v))
	}
	return nil
}

func (s *Settings) applyFlags(in Input) error {
	wrap := func(err error) error {
		if err != nil {
			return &Error{Kind: KindInput, Err: err}
		}
		return nil
	}
	if in.Set["timeout"] {
		if err := wrap(checkDuration("--timeout", in.Timeout, MinTimeout, MaxTimeout)); err != nil {
			return err
		}
		s.Timeout = in.Timeout
	}
	if in.Set["retry-delay"] {
		if err := wrap(checkDuration("--retry-delay", in.RetryDelay, 0, MaxRetryDelay)); err != nil {
			return err
		}
		s.RetryDelay = in.RetryDelay
	}
	if in.Set["retries"] {
		if err := wrap(setInt("--retries", in.Retries, &s.Retries, 0, MaxRetries)); err != nil {
			return err
		}
	}
	if in.Set["concurrency"] {
		if err := wrap(setInt("--concurrency", in.Concurrency, &s.Concurrency, 1, MaxConcurrency)); err != nil {
			return err
		}
	}
	if in.Set["protocol"] {
		if err := wrap(s.setProtocol("--protocol", in.Protocol)); err != nil {
			return err
		}
	}
	if in.Set["no-tcp-fallback"] {
		s.TCPFallback = !in.NoTCPFallback
	}
	if in.Set["compare-ttl"] {
		s.CompareTTL = in.CompareTTL
	}
	if in.Set["output"] {
		s.Output = strings.ToLower(strings.TrimSpace(in.Output))
	}
	return nil
}

func setDuration(name, v string, dst *time.Duration, lo, hi time.Duration) error {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("%s: invalid duration %q (examples: 500ms, 3s)", name, v)
	}
	if err := checkDuration(name, d, lo, hi); err != nil {
		return err
	}
	*dst = d
	return nil
}

func checkDuration(name string, d, lo, hi time.Duration) error {
	if d < lo || d > hi {
		return fmt.Errorf("%s: %s is out of range [%s, %s]", name, d, lo, hi)
	}
	return nil
}

func setInt(name string, n int, dst *int, lo, hi int) error {
	if n < lo || n > hi {
		return fmt.Errorf("%s: %d is out of range [%d, %d]", name, n, lo, hi)
	}
	*dst = n
	return nil
}

func (s *Settings) setProtocol(name, v string) error {
	switch p := strings.ToLower(strings.TrimSpace(v)); p {
	case dnsclient.UDP, dnsclient.TCP:
		s.Protocol = p
		return nil
	}
	return fmt.Errorf("%s: invalid protocol %q: must be udp or tcp", name, v)
}

// resolveQuery validates the query name and type. For PTR queries an IP
// address is converted to its in-addr.arpa or ip6.arpa name.
func (s *Settings) resolveQuery(in Input) error {
	t, err := normalize.ParseType(cmp.Or(strings.TrimSpace(in.Type), DefaultType))
	if err != nil {
		return &Error{Kind: KindInput, Err: fmt.Errorf("--type: %w", err)}
	}
	s.Type = t

	host := strings.TrimSpace(in.Host)
	s.Host = host
	if host == "" {
		return errorf(KindInput, "--host is required")
	}
	if t == dns.TypePTR {
		if addr, err := netip.ParseAddr(host); err == nil {
			// Zones (fe80::1%en0) are local and not part of the reverse name.
			if host, err = dns.ReverseAddr(addr.WithZone("").Unmap().String()); err != nil {
				return errorf(KindInput, "--host: cannot build reverse name for %s: %v", addr, err)
			}
		}
	}
	name, err := ValidateName(host)
	if err != nil {
		return &Error{Kind: KindInput, Err: fmt.Errorf("--host: %w", err)}
	}
	s.QueryName = name
	return nil
}

// ValidateName checks that s is a syntactically valid DNS query name and
// returns it fully qualified. Underscores (SRV and similar names), reverse
// names and a trailing dot are accepted; whitespace and control characters
// are not.
func ValidateName(s string) (string, error) {
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("invalid DNS name %q: contains whitespace or control characters", s)
	}
	fqdn := dns.Fqdn(s)
	if _, ok := dns.IsDomainName(fqdn); !ok {
		return "", fmt.Errorf("invalid DNS name %q", s)
	}
	return fqdn, nil
}

func (s *Settings) resolveResolvers(in Input, cfg *fileConfig) error {
	type entry struct{ name, addr, origin string }
	var entries []entry
	for _, v := range in.Servers {
		name, addr, found := strings.Cut(v, "=")
		if !found {
			name, addr = "", v
		}
		entries = append(entries, entry{name, addr, "--server"})
	}
	if in.ServersFile != "" {
		fromFile, err := readServersFile(in.ServersFile)
		if err != nil {
			return err
		}
		for _, e := range fromFile {
			entries = append(entries, entry{e[0], e[1], e[2]})
		}
	}
	kind := KindInput
	if len(entries) == 0 {
		// The configuration file's servers are used only when none were
		// given on the command line.
		kind = KindConfig
		for i, srv := range cfg.Servers {
			entries = append(entries, entry{srv.Name, srv.Address, fmt.Sprintf("config file servers[%d]", i)})
		}
	}
	if len(entries) == 0 {
		builtin, err := parseServers(defaultResolvers, "built-in default resolvers")
		if err != nil {
			return err
		}
		for _, e := range builtin {
			entries = append(entries, entry{e[0], e[1], e[2]})
		}
		s.Notes = append(s.Notes, Note{NoteDefaultResolvers, fmt.Sprintf(
			"no resolvers given: using the %d built-in public resolvers (use --server, --servers-file or a configuration file to choose your own)",
			len(builtin))})
	}

	seen := map[string]dnsclient.Resolver{}
	for _, e := range entries {
		r, err := dnsclient.ParseResolver(e.name, e.addr)
		if err != nil {
			return errorf(kind, "%s: %v", e.origin, err)
		}
		if first, dup := seen[r.Key()]; dup {
			s.Notes = append(s.Notes, Note{NoteDuplicate,
				fmt.Sprintf("duplicate resolver %s (%s) ignored: same endpoint as %s", r, e.origin, first)})
			continue
		}
		seen[r.Key()] = r
		s.Resolvers = append(s.Resolvers, r)
	}
	if len(s.Resolvers) > MaxResolvers {
		return errorf(kind, "too many resolvers: %d (maximum %d)", len(s.Resolvers), MaxResolvers)
	}
	return nil
}

func readServersFile(path string) ([][3]string, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, errorf(KindInput, "--servers-file: %v", err)
	}
	return parseServers(data, path)
}

// parseServers returns [name, address, origin] triples. Each non-empty,
// non-comment line is "ADDRESS" or "NAME ADDRESS".
func parseServers(data []byte, path string) ([][3]string, error) {
	var out [][3]string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		origin := fmt.Sprintf("%s:%d", path, n)
		switch f := strings.Fields(line); len(f) {
		case 1:
			out = append(out, [3]string{"", f[0], origin})
		case 2:
			out = append(out, [3]string{f[0], f[1], origin})
		default:
			return nil, errorf(KindInput, "%s: expected \"ADDRESS\" or \"NAME ADDRESS\", got %q", origin, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, errorf(KindInput, "--servers-file: %v", err)
	}
	return out, nil
}

func (s *Settings) resolveExpected(in Input) error {
	values := in.Expected
	if in.ExpectedFile != "" {
		data, err := readFile(in.ExpectedFile)
		if err != nil {
			return errorf(KindInput, "--expected-file: %v", err)
		}
		var ef expectedFile
		if err := decodeYAML(data, &ef); err != nil {
			return errorf(KindInput, "--expected-file %s: %v", in.ExpectedFile, err)
		}
		values = append(values, ef.Records...)
		// An expected file with an empty list expects NOERROR with no records.
		s.Expected = []normalize.Record{}
	}
	if len(values) > MaxExpected {
		return errorf(KindInput, "too many expected values: %d (maximum %d)", len(values), MaxExpected)
	}
	for _, v := range values {
		rec, err := normalize.ParseExpected(s.QueryName, s.Type, v)
		if err != nil {
			return errorf(KindInput, "--expected: %v", err)
		}
		s.Expected = append(s.Expected, rec)
	}
	return nil
}

// ExportFormatFor returns the export format for a path: explicit when given,
// otherwise inferred from the extension (.json, .yaml, .yml).
func ExportFormatFor(path, explicit string) (string, error) {
	if explicit != "" {
		switch f := strings.ToLower(strings.TrimSpace(explicit)); f {
		case "json", "yaml":
			return f, nil
		}
		return "", fmt.Errorf("invalid --export-format %q: must be json or yaml", explicit)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json", nil
	case ".yaml", ".yml":
		return "yaml", nil
	}
	return "", fmt.Errorf("cannot infer export format from %q: use a .json, .yaml or .yml extension or --export-format", path)
}

func (s *Settings) resolveExport(in Input) error {
	if in.Export == "" {
		if in.ExportFormat != "" {
			return errorf(KindInput, "--export-format requires --export")
		}
		return nil
	}
	f, err := ExportFormatFor(in.Export, in.ExportFormat)
	if err != nil {
		return &Error{Kind: KindExport, Err: err}
	}
	s.Export, s.ExportFormat = in.Export, f
	if st, err := os.Stat(filepath.Dir(in.Export)); err != nil || !st.IsDir() {
		return errorf(KindExport, "export directory %q does not exist", filepath.Dir(in.Export))
	}
	if st, err := os.Lstat(in.Export); err == nil {
		if !st.Mode().IsRegular() {
			return errorf(KindExport, "export destination %q exists and is not a regular file", in.Export)
		}
		if !in.Overwrite {
			return errorf(KindExport, "export destination %q already exists (use --overwrite to replace it)", in.Export)
		}
	}
	return nil
}

func loadConfig(path string, cfg *fileConfig) error {
	data, err := readFile(path)
	if err != nil {
		return errorf(KindConfig, "config file: %v", err)
	}
	if err := decodeYAML(data, cfg); err != nil {
		return errorf(KindConfig, "config file %s: %v", path, err)
	}
	return nil
}

// decodeYAML decodes strictly: unknown fields are errors. Empty input is valid.
func decodeYAML(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// readFile reads a regular file of at most MaxFileSize bytes.
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxFileSize)
	}
	return data, nil
}
