// Package output renders a consistency report as a human-readable table or
// as a stable JSON/YAML document, and exports the document to a file.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kmpoltorak/dns-consistency-checker/internal/compare"
	"github.com/kmpoltorak/dns-consistency-checker/internal/dnsclient"
)

// SchemaVersion is incremented on incompatible changes to Document.
const SchemaVersion = 1

// Meta describes the check that produced a report.
type Meta struct {
	Version     string
	QueryName   string
	QueryType   string
	Protocol    string
	TCPFallback bool
	Timeout     time.Duration
	Retries     int
	Verbose     bool // table only: extra columns
}

// Document is the stable machine-readable result (JSON and YAML).
type Document struct {
	SchemaVersion int          `json:"schema_version" yaml:"schema_version"`
	Tool          Tool         `json:"tool" yaml:"tool"`
	Query         Query        `json:"query" yaml:"query"`
	Summary       Summary      `json:"summary" yaml:"summary"`
	Expected      *ExpectedDoc `json:"expected,omitempty" yaml:"expected,omitempty"`
	Groups        []GroupDoc   `json:"groups" yaml:"groups"`
	Results       []ResultDoc  `json:"results" yaml:"results"`
	Issues        []IssueDoc   `json:"issues" yaml:"issues"`
}

// Tool identifies the producer.
type Tool struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
}

// Query describes what was asked and how.
type Query struct {
	Name        string `json:"name" yaml:"name"`
	Type        string `json:"type" yaml:"type"`
	Protocol    string `json:"protocol" yaml:"protocol"`
	TCPFallback bool   `json:"tcp_fallback" yaml:"tcp_fallback"`
	CompareTTL  bool   `json:"compare_ttl" yaml:"compare_ttl"`
	TimeoutMS   int64  `json:"timeout_ms" yaml:"timeout_ms"`
	Retries     int    `json:"retries" yaml:"retries"`
}

// Summary is the overall outcome.
type Summary struct {
	Status         string   `json:"status" yaml:"status"`
	ResolversTotal int      `json:"resolvers_total" yaml:"resolvers_total"`
	Successful     int      `json:"successful" yaml:"successful"`
	Failed         int      `json:"failed" yaml:"failed"`
	Groups         int      `json:"groups" yaml:"groups"`
	MajorityGroup  int      `json:"majority_group" yaml:"majority_group"` // 0: no majority
	Outliers       []string `json:"outliers" yaml:"outliers"`
	TCPFallback    []string `json:"tcp_fallback" yaml:"tcp_fallback"` // resolvers retried over TCP after a truncated UDP answer
}

// ExpectedDoc is the expected-mode result.
type ExpectedDoc struct {
	Records              []string `json:"records" yaml:"records"`
	Matching             int      `json:"matching" yaml:"matching"`
	NonMatching          int      `json:"non_matching" yaml:"non_matching"`
	Failed               int      `json:"failed" yaml:"failed"`
	MatchingResolvers    []string `json:"matching_resolvers" yaml:"matching_resolvers"`
	NonMatchingResolvers []string `json:"non_matching_resolvers" yaml:"non_matching_resolvers"`
	FailedResolvers      []string `json:"failed_resolvers" yaml:"failed_resolvers"`
}

// GroupDoc is a group of resolvers with equivalent answers.
type GroupDoc struct {
	ID         int      `json:"id" yaml:"id"`
	Status     string   `json:"status" yaml:"status"`
	CNAMEChain []string `json:"cname_chain" yaml:"cname_chain"`
	Answers    []string `json:"answers" yaml:"answers"`
	Resolvers  []string `json:"resolvers" yaml:"resolvers"`
	Count      int      `json:"count" yaml:"count"`
}

// ResolverDoc identifies a resolver.
type ResolverDoc struct {
	Name    string `json:"name" yaml:"name"`
	Host    string `json:"host" yaml:"host"`       // hostname as given; empty for IP literals
	Address string `json:"address" yaml:"address"` // IP:PORT or [IPv6]:PORT; empty if a hostname could not be resolved
}

// FlagsDoc are the response header flags.
type FlagsDoc struct {
	Authoritative      bool `json:"authoritative" yaml:"authoritative"`
	Truncated          bool `json:"truncated" yaml:"truncated"`
	RecursionDesired   bool `json:"recursion_desired" yaml:"recursion_desired"`
	RecursionAvailable bool `json:"recursion_available" yaml:"recursion_available"`
	AuthenticatedData  bool `json:"authenticated_data" yaml:"authenticated_data"`
	CheckingDisabled   bool `json:"checking_disabled" yaml:"checking_disabled"`
}

// AnswerDoc is one normalized record.
type AnswerDoc struct {
	Name  string `json:"name" yaml:"name"`
	Type  string `json:"type" yaml:"type"`
	Value string `json:"value" yaml:"value"`
	TTL   uint32 `json:"ttl" yaml:"ttl"`
	Raw   string `json:"raw" yaml:"raw"`
}

// ErrorDoc describes a failure.
type ErrorDoc struct {
	Category string `json:"category" yaml:"category"`
	Message  string `json:"message" yaml:"message"`
}

// ResultDoc is the outcome for one resolver.
type ResultDoc struct {
	Resolver        ResolverDoc `json:"resolver" yaml:"resolver"`
	QueryName       string      `json:"query_name" yaml:"query_name"`
	QueryType       string      `json:"query_type" yaml:"query_type"`
	Status          string      `json:"status" yaml:"status"`
	ProtocolInitial string      `json:"protocol_initial" yaml:"protocol_initial"`
	ProtocolFinal   string      `json:"protocol_final" yaml:"protocol_final"`
	DurationMS      int64       `json:"duration_ms" yaml:"duration_ms"`
	Attempts        int         `json:"attempts" yaml:"attempts"`
	Timestamp       string      `json:"timestamp" yaml:"timestamp"`
	Flags           FlagsDoc    `json:"flags" yaml:"flags"`
	CNAMEChain      []string    `json:"cname_chain" yaml:"cname_chain"`
	FinalName       string      `json:"final_name" yaml:"final_name"`
	Answers         []AnswerDoc `json:"answers" yaml:"answers"`
	IgnoredRecords  int         `json:"ignored_records" yaml:"ignored_records"`
	Group           int         `json:"group" yaml:"group"` // 0: failed
	Error           *ErrorDoc   `json:"error,omitempty" yaml:"error,omitempty"`
}

// IssueDoc is one finding.
type IssueDoc struct {
	Resolver string `json:"resolver" yaml:"resolver"`
	Type     string `json:"type" yaml:"type"`
	Severity string `json:"severity" yaml:"severity"`
	Message  string `json:"message" yaml:"message"`
}

// NewDocument converts a report into the stable document. All slices are
// non-nil so they serialize as empty arrays.
func NewDocument(rep *compare.Report, m Meta) Document {
	names := func(idx []int) []string {
		out := make([]string, 0, len(idx))
		for _, i := range idx {
			out = append(out, rep.Results[i].Resolver.String())
		}
		return out
	}
	doc := Document{
		SchemaVersion: SchemaVersion,
		Tool:          Tool{Name: "dns-consistency-checker", Version: m.Version},
		Query: Query{
			Name: m.QueryName, Type: m.QueryType, Protocol: m.Protocol, TCPFallback: m.TCPFallback,
			CompareTTL: rep.CompareTTL, TimeoutMS: m.Timeout.Milliseconds(), Retries: m.Retries,
		},
		Summary: Summary{
			Status:         string(rep.Status),
			ResolversTotal: len(rep.Results),
			Successful:     rep.Successful,
			Failed:         rep.Failed,
			Groups:         len(rep.Groups),
			Outliers:       names(rep.Outliers),
			TCPFallback:    names(rep.TCPFallback),
		},
		Groups:  make([]GroupDoc, 0, len(rep.Groups)),
		Results: make([]ResultDoc, 0, len(rep.Results)),
		Issues:  make([]IssueDoc, 0, len(rep.Issues)),
	}
	if g := rep.MajorityGroup(); g != nil {
		doc.Summary.MajorityGroup = g.ID
	}
	if e := rep.Expected; e != nil {
		doc.Expected = &ExpectedDoc{
			Records:  make([]string, 0, len(e.Records)),
			Matching: len(e.Matching), NonMatching: len(e.NonMatching), Failed: len(e.Failed),
			MatchingResolvers: names(e.Matching), NonMatchingResolvers: names(e.NonMatching), FailedResolvers: names(e.Failed),
		}
		for _, r := range e.Records {
			doc.Expected.Records = append(doc.Expected.Records, r.Value)
		}
	}
	for _, g := range rep.Groups {
		gd := GroupDoc{ID: g.ID, Status: string(g.Status), CNAMEChain: nonNil(g.CNAMEChain),
			Answers: make([]string, 0, len(g.Records)), Resolvers: names(g.Members), Count: len(g.Members)}
		for _, r := range g.Records {
			gd.Answers = append(gd.Answers, r.Value)
		}
		doc.Groups = append(doc.Groups, gd)
	}
	for i, r := range rep.Results {
		doc.Results = append(doc.Results, newResultDoc(r, rep.GroupOf(i)))
	}
	for _, is := range rep.Issues {
		doc.Issues = append(doc.Issues, IssueDoc{Resolver: is.Resolver, Type: string(is.Type), Severity: string(is.Severity), Message: is.Message})
	}
	return doc
}

func newResultDoc(r dnsclient.Result, group int) ResultDoc {
	rd := ResultDoc{
		Resolver:        ResolverDoc{Name: r.Resolver.Name, Host: r.Resolver.Host},
		QueryName:       r.QueryName,
		QueryType:       r.QueryType,
		Status:          string(r.Status),
		ProtocolInitial: r.ProtocolInitial,
		ProtocolFinal:   r.ProtocolFinal,
		DurationMS:      r.Duration.Milliseconds(),
		Attempts:        r.Attempts,
		Timestamp:       r.Time.UTC().Format(time.RFC3339Nano),
		Flags:           FlagsDoc(r.Flags),
		CNAMEChain:      nonNil(r.CNAMEChain),
		FinalName:       r.FinalName,
		Answers:         make([]AnswerDoc, 0, len(r.Records)),
		IgnoredRecords:  r.IgnoredRecords,
		Group:           group,
	}
	if r.Resolver.Resolved() {
		rd.Resolver.Address = r.Resolver.Endpoint.String()
	}
	for _, rec := range r.Records {
		rd.Answers = append(rd.Answers, AnswerDoc{Name: rec.Name, Type: rec.Type, Value: rec.Value, TTL: rec.TTL, Raw: rec.Raw})
	}
	if !r.Status.Usable() {
		rd.Error = &ErrorDoc{Category: strings.ToLower(string(r.Status)), Message: r.Error}
	}
	return rd
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Marshal encodes the document as "json" or "yaml", ending with a newline.
func Marshal(doc Document, format string) ([]byte, error) {
	switch format {
	case "json":
		b, err := json.MarshalIndent(doc, "", "  ")
		return append(b, '\n'), err
	case "yaml":
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return nil, err
		}
		err := enc.Close()
		return buf.Bytes(), err
	}
	return nil, fmt.Errorf("unknown format %q", format)
}

// Render writes the report to w as "table", "json" or "yaml".
func Render(w io.Writer, format string, rep *compare.Report, m Meta) error {
	if format == "table" {
		return writeTable(w, rep, m)
	}
	b, err := Marshal(NewDocument(rep, m), format)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// Export writes the full document to path. Without overwrite an existing
// file is never replaced (O_EXCL also closes the check-then-create race).
func Export(path, format string, overwrite bool, rep *compare.Report, m Meta) error {
	b, err := Marshal(NewDocument(rep, m), format)
	if err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
