// Package compare is the consistency engine: it groups normalized resolver
// answers, finds the majority response and outliers, analyses TTLs, matches
// expected data, produces issues and selects the overall status.
package compare

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/kmpoltorak/dns-consistency-checker/internal/dnsclient"
	"github.com/kmpoltorak/dns-consistency-checker/internal/normalize"
)

// Overall is the overall consistency status of a check.
type Overall string

// Overall statuses.
const (
	Consistent     Overall = "CONSISTENT"
	Inconsistent   Overall = "INCONSISTENT"
	PartialFailure Overall = "PARTIAL_FAILURE"
	TotalFailure   Overall = "TOTAL_FAILURE"
)

// Severity of an issue.
type Severity string

// Severities.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// IssueType identifies the kind of issue.
type IssueType string

// Issue types.
const (
	IssueTimeout          IssueType = "timeout"
	IssueNetworkError     IssueType = "network_error"
	IssueProtocolError    IssueType = "protocol_error"
	IssueServFail         IssueType = "servfail"
	IssueRefused          IssueType = "refused"
	IssueFormErr          IssueType = "formerr"
	IssueNotImp           IssueType = "notimp"
	IssueRcodeError       IssueType = "rcode_error"
	IssueDifferentRcode   IssueType = "different_rcode"
	IssueDifferentRRset   IssueType = "different_rrset"
	IssueTruncated        IssueType = "truncated"
	IssueExpectedMismatch IssueType = "expected_mismatch"
	IssueNoMajority       IssueType = "no_majority"
	IssueTTLDifference    IssueType = "ttl_difference"
	IssueDuplicate        IssueType = "duplicate_resolver"
	IssueTCPFallback      IssueType = "tcp_fallback"
	IssueIgnoredRecords   IssueType = "ignored_records"
)

// Issue is a human-readable finding. Resolver is empty for group-level issues.
type Issue struct {
	Resolver string
	Type     IssueType
	Severity Severity
	Message  string
}

// Group is a set of resolvers that returned equivalent answers.
type Group struct {
	ID         int // 1-based, in group order
	Status     dnsclient.Status
	CNAMEChain []string
	Records    []normalize.Record // as returned by the first member
	Members    []int              // indexes into Report.Results, in input order
	key        string
}

// Expected is the result of expected-data matching.
type Expected struct {
	Records     []normalize.Record // normalized expected RRset
	Matching    []int              // indexes into Report.Results
	NonMatching []int
	Failed      []int
}

// Options controls the analysis.
type Options struct {
	CompareTTL bool
	Expected   []normalize.Record // nil disables expected mode
	Duplicates []string           // notes about duplicate resolvers removed from the input
}

// Report is the full analysis of one check.
type Report struct {
	Results    []dnsclient.Result
	CompareTTL bool
	Groups     []Group // by size (descending), then comparison key
	Majority   int     // index into Groups, -1 when there is no unique largest group
	Outliers   []int   // indexes into Results of usable answers outside the majority
	Status     Overall
	Successful int
	Failed     int
	Expected   *Expected
	Issues     []Issue
}

// MajorityGroup returns the majority group or nil.
func (r *Report) MajorityGroup() *Group {
	if r.Majority < 0 {
		return nil
	}
	return &r.Groups[r.Majority]
}

// GroupOf returns the 1-based ID of the group containing result i, or 0.
func (r *Report) GroupOf(i int) int {
	for _, g := range r.Groups {
		if slices.Contains(g.Members, i) {
			return g.ID
		}
	}
	return 0
}

// Key returns the comparison key of a usable result: status, CNAME chain and
// the sorted record values, plus TTLs when compareTTL is set.
func Key(res dnsclient.Result, compareTTL bool) string {
	var b strings.Builder
	b.WriteString(string(res.Status))
	b.WriteByte(0)
	b.WriteString(strings.Join(res.CNAMEChain, " "))
	for _, rec := range res.Records {
		b.WriteByte(0)
		b.WriteString(rec.Value)
		if compareTTL {
			fmt.Fprintf(&b, "\x01%d", rec.TTL)
		}
	}
	return b.String()
}

// Analyze compares resolver results. The output depends only on its inputs.
func Analyze(results []dnsclient.Result, opts Options) Report {
	rep := Report{Results: results, CompareTTL: opts.CompareTTL, Majority: -1}

	byKey := map[string]int{}
	for i, res := range results {
		if !res.Status.Usable() {
			rep.Failed++
			continue
		}
		rep.Successful++
		k := Key(res, opts.CompareTTL)
		gi, ok := byKey[k]
		if !ok {
			gi = len(rep.Groups)
			byKey[k] = gi
			rep.Groups = append(rep.Groups, Group{Status: res.Status, CNAMEChain: res.CNAMEChain, Records: res.Records, key: k})
		}
		rep.Groups[gi].Members = append(rep.Groups[gi].Members, i)
	}
	slices.SortFunc(rep.Groups, func(a, b Group) int {
		return cmp.Or(cmp.Compare(len(b.Members), len(a.Members)), strings.Compare(a.key, b.key))
	})
	for i := range rep.Groups {
		rep.Groups[i].ID = i + 1
	}
	if len(rep.Groups) == 1 || len(rep.Groups) > 1 && len(rep.Groups[0].Members) > len(rep.Groups[1].Members) {
		rep.Majority = 0
		for _, g := range rep.Groups[1:] {
			rep.Outliers = append(rep.Outliers, g.Members...)
		}
		slices.Sort(rep.Outliers)
	}
	if opts.Expected != nil {
		rep.Expected = matchExpected(results, normalize.RRset(opts.Expected))
	}
	rep.Status = overall(&rep)
	rep.Issues = issues(&rep)
	for _, d := range opts.Duplicates {
		rep.Issues = append(rep.Issues, Issue{Type: IssueDuplicate, Severity: SeverityInfo, Message: d})
	}
	return rep
}

func matchExpected(results []dnsclient.Result, want []normalize.Record) *Expected {
	exp := &Expected{Records: want}
	for i, res := range results {
		switch {
		case !res.Status.Usable():
			exp.Failed = append(exp.Failed, i)
		case res.Status == dnsclient.StatusNoError && sameValues(res.Records, want):
			exp.Matching = append(exp.Matching, i)
		default:
			exp.NonMatching = append(exp.NonMatching, i)
		}
	}
	return exp
}

func sameValues(a, b []normalize.Record) bool {
	return slices.EqualFunc(a, b, func(x, y normalize.Record) bool { return x.Value == y.Value })
}

func overall(rep *Report) Overall {
	switch {
	case rep.Successful == 0:
		return TotalFailure
	case len(rep.Groups) > 1, rep.Expected != nil && len(rep.Expected.NonMatching) > 0:
		return Inconsistent
	case rep.Failed > 0:
		return PartialFailure
	default:
		return Consistent
	}
}

var failureIssues = map[dnsclient.Status]IssueType{
	dnsclient.StatusTimeout:       IssueTimeout,
	dnsclient.StatusNetworkError:  IssueNetworkError,
	dnsclient.StatusProtocolError: IssueProtocolError,
	dnsclient.StatusServFail:      IssueServFail,
	dnsclient.StatusRefused:       IssueRefused,
	dnsclient.StatusFormErr:       IssueFormErr,
	dnsclient.StatusNotImp:        IssueNotImp,
}

// issues lists per-resolver issues in input order, then group-level issues.
// Analyze appends duplicate-resolver notes last.
func issues(rep *Report) []Issue {
	var out []Issue
	add := func(res *dnsclient.Result, t IssueType, sev Severity, format string, args ...any) {
		iss := Issue{Type: t, Severity: sev, Message: fmt.Sprintf(format, args...)}
		if res != nil {
			iss.Resolver = res.Resolver.String()
		}
		out = append(out, iss)
	}
	maj := rep.MajorityGroup()
	for i := range rep.Results {
		res := &rep.Results[i]
		name := res.Resolver.String()
		if !res.Status.Usable() {
			t, ok := failureIssues[res.Status]
			if !ok {
				t = IssueRcodeError
			}
			switch res.Status {
			case dnsclient.StatusTimeout:
				add(res, t, SeverityError, "%s timed out: %s (%s)", name, res.Error, attempts(res.Attempts))
			case dnsclient.StatusNetworkError:
				add(res, t, SeverityError, "%s network error: %s (%s)", name, res.Error, attempts(res.Attempts))
			case dnsclient.StatusProtocolError:
				add(res, t, SeverityError, "%s protocol error: %s", name, res.Error)
			default:
				add(res, t, SeverityError, "%s returned %s", name, res.Status)
			}
			continue
		}
		if res.Flags.Truncated {
			add(res, IssueTruncated, SeverityWarning,
				"%s returned a truncated response (TC=1) and TCP fallback is disabled; the RRset may be incomplete", name)
		}
		if res.ProtocolFinal != res.ProtocolInitial {
			add(res, IssueTCPFallback, SeverityInfo, "%s returned a truncated UDP response (TC=1); the query was repeated over TCP", name)
		}
		if n := res.IgnoredRecords; n > 0 {
			add(res, IssueIgnoredRecords, SeverityInfo,
				"%s returned %d answer record(s) outside the CNAME chain or of another type; they were not compared", name, n)
		}
		if maj != nil && slices.Contains(rep.Outliers, i) {
			switch {
			case res.Status != maj.Status:
				add(res, IssueDifferentRcode, SeverityError, "%s returned %s while the majority returned %s", name, res.Status, maj.Status)
			case !slices.Equal(res.CNAMEChain, maj.CNAMEChain):
				add(res, IssueDifferentRRset, SeverityError, "%s returned a different CNAME chain than the majority", name)
			case !sameValues(res.Records, maj.Records):
				add(res, IssueDifferentRRset, SeverityError, "%s returned a different RRset than the majority", name)
			default:
				add(res, IssueDifferentRRset, SeverityError, "%s returned different TTLs than the majority", name)
			}
		}
		if rep.Expected != nil && slices.Contains(rep.Expected.NonMatching, i) {
			add(res, IssueExpectedMismatch, SeverityError, "%s did not match the expected RRset", name)
		}
	}
	if len(rep.Groups) > 1 && maj == nil {
		add(nil, IssueNoMajority, SeverityWarning,
			"no majority response: the %d largest response groups are tied (%s each)", tied(rep.Groups), countResolvers(len(rep.Groups[0].Members)))
	}
	if !rep.CompareTTL {
		for _, g := range rep.Groups {
			if d := ttlSpread(rep.Results, g); d != "" {
				add(nil, IssueTTLDifference, SeverityInfo, "TTL values differ within response group %d: %s", g.ID, d)
			}
		}
	}
	return out
}

// ttlSpread summarizes TTL differences between members of a group, e.g.
// "TTL 120-300s on 2 of 3 records", or returns "" when all TTLs agree.
func ttlSpread(results []dnsclient.Result, g Group) string {
	differing := 0
	var lo, hi uint32
	for ri, rec := range g.Records {
		rlo, rhi := rec.TTL, rec.TTL
		for _, m := range g.Members {
			// Members share the same sorted values, so record ri lines up.
			ttl := results[m].Records[ri].TTL
			rlo, rhi = min(rlo, ttl), max(rhi, ttl)
		}
		if rlo != rhi {
			if differing == 0 {
				lo, hi = rlo, rhi
			}
			differing++
			lo, hi = min(lo, rlo), max(hi, rhi)
		}
	}
	if differing == 0 {
		return ""
	}
	return fmt.Sprintf("TTL %d-%ds on %d of %d records (per-record TTLs are in the structured output)",
		lo, hi, differing, len(g.Records))
}

// tied returns how many groups share the largest size.
func tied(groups []Group) int {
	n := 0
	for _, g := range groups {
		if len(g.Members) == len(groups[0].Members) {
			n++
		}
	}
	return n
}

func countResolvers(n int) string {
	if n == 1 {
		return "1 resolver"
	}
	return fmt.Sprintf("%d resolvers", n)
}

func attempts(n int) string {
	if n == 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("%d attempts", n)
}
