// Package cli implements the dns-consistency-checker command line: command
// dispatch, flag parsing, help text, orchestration and exit codes.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/miekg/dns"

	"github.com/kmpoltorak/dns-consistency-checker/internal/compare"
	"github.com/kmpoltorak/dns-consistency-checker/internal/config"
	"github.com/kmpoltorak/dns-consistency-checker/internal/dnsclient"
	"github.com/kmpoltorak/dns-consistency-checker/internal/output"
)

// Exit codes.
const (
	ExitConsistent     = 0
	ExitInconsistent   = 1
	ExitPartialFailure = 2
	ExitTotalFailure   = 3
	ExitInvalidInput   = 4
	ExitConfigError    = 5
	ExitExportError    = 6
	ExitInternalError  = 7
)

// Version is set at build time with -ldflags "-X .../internal/cli.Version=...".
var Version = "dev"

const name = "dns-consistency-checker"

// ExitCode maps an overall status to its exit code.
func ExitCode(s compare.Overall) int {
	switch s {
	case compare.Consistent:
		return ExitConsistent
	case compare.Inconsistent:
		return ExitInconsistent
	case compare.PartialFailure:
		return ExitPartialFailure
	case compare.TotalFailure:
		return ExitTotalFailure
	}
	return ExitInternalError
}

func errorExitCode(err error) int {
	var ce *config.Error
	if errors.As(err, &ce) {
		switch ce.Kind {
		case config.KindConfig:
			return ExitConfigError
		case config.KindExport:
			return ExitExportError
		}
	}
	return ExitInvalidInput
}

// Run executes the command line args (without the program name) and returns
// the process exit code. Results go to stdout, diagnostics to stderr.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, mainHelp)
		return ExitInvalidInput
	}
	switch args[0] {
	case "check":
		return runCheck(ctx, args[1:], stdout, stderr, getenv)
	case "version", "--version", "-version":
		fmt.Fprintf(stdout, "%s %s\n", name, Version)
		return ExitConsistent
	case "help", "--help", "-help", "-h":
		topic := ""
		if len(args) > 1 {
			topic = args[1]
		}
		switch topic {
		case "check":
			fmt.Fprint(stdout, checkHelp)
		case "", "help", "version":
			fmt.Fprint(stdout, mainHelp)
		default:
			fmt.Fprintf(stderr, "error: unknown command %q\n", topic)
			return ExitInvalidInput
		}
		return ExitConsistent
	}
	fmt.Fprintf(stderr, "error: unknown command %q\nRun '%s help' for usage.\n", args[0], name)
	return ExitInvalidInput
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func parseCheckFlags(args []string) (config.Input, error) {
	in := config.Input{Set: map[string]bool{}}
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&in.Host, "host", "", "")
	fs.StringVar(&in.Type, "type", config.DefaultType, "")
	fs.Var((*multiFlag)(&in.Servers), "server", "")
	fs.StringVar(&in.ServersFile, "servers-file", "", "")
	fs.StringVar(&in.ConfigFile, "config", "", "")
	fs.StringVar(&in.Protocol, "protocol", config.DefaultProtocol, "")
	fs.DurationVar(&in.Timeout, "timeout", config.DefaultTimeout, "")
	fs.IntVar(&in.Retries, "retries", config.DefaultRetries, "")
	fs.DurationVar(&in.RetryDelay, "retry-delay", config.DefaultRetryDelay, "")
	fs.IntVar(&in.Concurrency, "concurrency", config.DefaultConcurrency, "")
	fs.BoolVar(&in.CompareTTL, "compare-ttl", false, "")
	fs.BoolVar(&in.NoTCPFallback, "no-tcp-fallback", false, "")
	fs.Var((*multiFlag)(&in.Expected), "expected", "")
	fs.StringVar(&in.ExpectedFile, "expected-file", "", "")
	fs.StringVar(&in.Output, "output", config.DefaultOutput, "")
	fs.StringVar(&in.Export, "export", "", "")
	fs.StringVar(&in.ExportFormat, "export-format", "", "")
	fs.BoolVar(&in.Overwrite, "overwrite", false, "")
	fs.BoolVar(&in.Verbose, "verbose", false, "")
	fs.BoolVar(&in.Verbose, "v", false, "")
	if err := fs.Parse(args); err != nil {
		return in, err
	}
	if fs.NArg() > 0 {
		return in, fmt.Errorf("unexpected argument %q (all options are flags, e.g. --host %s)", fs.Arg(0), fs.Arg(0))
	}
	fs.Visit(func(f *flag.Flag) { in.Set[f.Name] = true })
	return in, nil
}

func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	in, err := parseCheckFlags(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, checkHelp)
		return ExitConsistent
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\nRun '%s help check' for usage.\n", err, name)
		return ExitInvalidInput
	}
	s, err := config.Resolve(in, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return errorExitCode(err)
	}

	level := slog.LevelWarn
	if s.Verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	log.Debug("starting check", "query", s.QueryName, "type", dns.TypeToString[s.Type], "resolvers", len(s.Resolvers),
		"protocol", s.Protocol, "tcp_fallback", s.TCPFallback, "timeout", s.Timeout, "retries", s.Retries,
		"retry_delay", s.RetryDelay, "concurrency", s.Concurrency)

	results := dnsclient.QueryAll(ctx, s.Resolvers, s.QueryName, s.Type, dnsclient.Options{
		Protocol:    s.Protocol,
		TCPFallback: s.TCPFallback,
		Timeout:     s.Timeout,
		Retries:     s.Retries,
		RetryDelay:  s.RetryDelay,
		Concurrency: s.Concurrency,
		Logger:      log,
	})
	rep := compare.Analyze(results, compare.Options{CompareTTL: s.CompareTTL, Expected: s.Expected, Duplicates: s.Duplicates})
	meta := output.Meta{
		Version: Version, QueryName: s.QueryName, QueryType: dns.TypeToString[s.Type], Protocol: s.Protocol,
		TCPFallback: s.TCPFallback, Timeout: s.Timeout, Retries: s.Retries, Verbose: s.Verbose,
	}

	if err := output.Render(stdout, s.Output, &rep, meta); err != nil {
		fmt.Fprintf(stderr, "error: writing output: %v\n", err)
		return ExitInternalError
	}
	if s.Export != "" {
		if err := output.Export(s.Export, s.ExportFormat, s.Overwrite, &rep, meta); err != nil {
			fmt.Fprintf(stderr, "error: export failed: %v\n", err)
			return ExitExportError
		}
		log.Debug("exported result", "path", s.Export, "format", s.ExportFormat)
	}
	return ExitCode(rep.Status)
}
