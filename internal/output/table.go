package output

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kmpoltorak/dns-consistency-checker/internal/compare"
	"github.com/kmpoltorak/dns-consistency-checker/internal/dnsclient"
)

// writeTable renders the human-readable report. Rows follow resolver input
// order; multi-record answers continue on following lines.
func writeTable(out io.Writer, rep *compare.Report, m Meta) error {
	w := bufio.NewWriter(out)
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }

	p("DNS Consistency Check\n=====================\n\n")
	p("Query:\n%s %s\n\n", m.QueryName, m.QueryType)
	proto := strings.ToUpper(m.Protocol)
	if m.Protocol == dnsclient.UDP {
		if m.TCPFallback {
			proto += " (TCP fallback on truncation)"
		} else {
			proto += " (no TCP fallback)"
		}
	}
	p("Protocol:\n%s\n\n", proto)
	p("Resolvers checked: %d\n\n", len(rep.Results))

	named := slices.ContainsFunc(rep.Results, func(r dnsclient.Result) bool { return r.Resolver.Name != "" })
	var header []string
	if named {
		header = append(header, "NAME", "ADDRESS")
	} else {
		header = append(header, "SERVER")
	}
	header = append(header, "STATUS", "RESPONSE", "TIME")
	if m.Verbose {
		header = append(header, "PROTO", "ATTEMPTS", "FLAGS")
	}

	var rows [][]string
	for _, r := range rep.Results {
		lines := responseLines(r)
		row := []string{}
		if named {
			row = append(row, cmp.Or(r.Resolver.Name, "-"), r.Resolver.Address())
		} else {
			row = append(row, r.Resolver.Address())
		}
		row = append(row, string(r.Status), lines[0], formatDuration(r.Duration))
		if m.Verbose {
			proto := r.ProtocolFinal
			if r.ProtocolFinal != r.ProtocolInitial {
				proto = r.ProtocolInitial + "->" + r.ProtocolFinal
			}
			row = append(row, proto, fmt.Sprint(r.Attempts), flagString(r.Flags))
		}
		rows = append(rows, row)
		respCol := len(header) - 2
		if m.Verbose {
			respCol = len(header) - 5
		}
		for _, l := range lines[1:] {
			cont := make([]string, len(header))
			cont[respCol] = l
			rows = append(rows, cont)
		}
	}

	// Render the table first to size the separator line to its width.
	var tb strings.Builder
	tw := tabwriter.NewWriter(&tb, 0, 0, 3, ' ', 0)
	for _, row := range append([][]string{header}, rows...) {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
	lines := strings.Split(strings.TrimSuffix(tb.String(), "\n"), "\n")
	width := 0
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
		width = max(width, len(lines[i]))
	}
	p("%s\n%s\n%s\n", lines[0], strings.Repeat("-", width), strings.Join(lines[1:], "\n"))

	p("\nConsistency:\n%s\n\n", rep.Status)
	p("Successful: %d\nFailed: %d\n", rep.Successful, rep.Failed)

	if g := rep.MajorityGroup(); g != nil && len(rep.Groups) > 1 {
		p("\nMajority response (%d of %d resolvers; not necessarily correct):\n", len(g.Members), len(rep.Results))
		writeGroupAnswer(p, g)
	}
	if len(rep.Groups) > 1 {
		p("\nResponse groups:\n")
		for _, g := range rep.Groups {
			p("  Group %d (%d resolver%s):\n", g.ID, len(g.Members), plural(len(g.Members)))
			writeGroupAnswer(func(format string, args ...any) { p("  "+format, args...) }, &g)
			var names []string
			for _, i := range g.Members {
				names = append(names, rep.Results[i].Resolver.String())
			}
			p("    resolvers: %s\n", strings.Join(names, ", "))
		}
	}

	if e := rep.Expected; e != nil {
		p("\nExpected RRset:\n")
		if len(e.Records) == 0 {
			p("  (no records)\n")
		}
		for _, r := range e.Records {
			p("  %s\n", r.Value)
		}
		p("\nMatching resolvers: %d\nNon-matching resolvers: %d\nFailed resolvers: %d\n",
			len(e.Matching), len(e.NonMatching), len(e.Failed))
	}

	if len(rep.Issues) > 0 {
		p("\nIssues:\n")
		for _, is := range rep.Issues {
			prefix := ""
			if is.Severity != compare.SeverityError {
				prefix = "[" + string(is.Severity) + "] "
			}
			p("- %s%s\n", prefix, is.Message)
		}
	}
	return w.Flush()
}

func writeGroupAnswer(p func(string, ...any), g *compare.Group) {
	if g.Status != dnsclient.StatusNoError {
		p("  %s\n", g.Status)
	}
	for _, c := range g.CNAMEChain {
		p("  CNAME %s\n", c)
	}
	if g.Status == dnsclient.StatusNoError && len(g.Records) == 0 {
		p("  (no records)\n")
	}
	for _, r := range g.Records {
		p("  %s\n", r.Value)
	}
}

// responseLines returns the RESPONSE column: CNAME chain, then records.
func responseLines(r dnsclient.Result) []string {
	var lines []string
	for _, c := range r.CNAMEChain {
		lines = append(lines, "CNAME "+c)
	}
	for _, rec := range r.Records {
		lines = append(lines, rec.Value)
	}
	if len(lines) == 0 {
		if r.Status == dnsclient.StatusNoError {
			return []string{"(no records)"}
		}
		return []string{"-"}
	}
	return lines
}

func flagString(f dnsclient.Flags) string {
	var out []string
	for _, x := range []struct {
		on   bool
		name string
	}{{f.Authoritative, "aa"}, {f.Truncated, "tc"}, {f.RecursionDesired, "rd"}, {f.RecursionAvailable, "ra"},
		{f.AuthenticatedData, "ad"}, {f.CheckingDisabled, "cd"}} {
		if x.on {
			out = append(out, x.name)
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, " ")
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(10 * time.Millisecond).String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
