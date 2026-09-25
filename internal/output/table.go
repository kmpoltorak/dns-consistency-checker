package output

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
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
	for i, r := range rep.Results {
		lines := responseLines(r)
		row := []string{}
		if named {
			row = append(row, cmp.Or(r.Resolver.Name, "-"), r.Resolver.Address())
		} else {
			row = append(row, r.Resolver.Address())
		}
		row = append(row, resultIcon(rep, i)+" "+string(r.Status), lines[0], formatDuration(r.Duration))
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

	lines, width := alignColumns(append([][]string{header}, rows...))
	p("%s\n%s\n%s\n", lines[0], strings.Repeat("-", width), strings.Join(lines[1:], "\n"))
	p("\n%s agrees   %s different answer   %s failed\n", iconOK, iconDiffers, iconFailed)

	p("\nConsistency:\n%s %s\n\n", overallIcons[rep.Status], rep.Status)
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

// Status icons. Each is a single rune displayed two columns wide.
const (
	iconOK      = "✅"
	iconDiffers = "❌"
	iconFailed  = "🚫"
)

var overallIcons = map[compare.Overall]string{
	compare.Consistent:     iconOK,
	compare.Inconsistent:   iconDiffers,
	compare.PartialFailure: "⚠️",
	compare.TotalFailure:   iconFailed,
}

// resultIcon marks result i: failed, different from the majority (or no
// majority exists) or from the expected RRset, or agreeing.
func resultIcon(rep *compare.Report, i int) string {
	if !rep.Results[i].Status.Usable() {
		return iconFailed
	}
	if rep.Expected != nil && slices.Contains(rep.Expected.NonMatching, i) {
		return iconDiffers
	}
	if maj := rep.MajorityGroup(); len(rep.Groups) > 1 && (maj == nil || !slices.Contains(maj.Members, i)) {
		return iconDiffers
	}
	return iconOK
}

// alignColumns pads cells to their column's display width (three spaces
// between columns) and returns the lines and the widest line's width.
// text/tabwriter counts runes, which misaligns two-column-wide icons.
func alignColumns(rows [][]string) ([]string, int) {
	var widths []int
	for _, row := range rows {
		for c, cell := range row {
			if c == len(widths) {
				widths = append(widths, 0)
			}
			widths[c] = max(widths[c], displayWidth(cell))
		}
	}
	lines := make([]string, len(rows))
	maxWidth := 0
	for i, row := range rows {
		var b strings.Builder
		for c, cell := range row {
			b.WriteString(cell)
			if c < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[c]-displayWidth(cell)+3))
			}
		}
		lines[i] = strings.TrimRight(b.String(), " ")
		maxWidth = max(maxWidth, displayWidth(lines[i]))
	}
	return lines, maxWidth
}

// displayWidth is the terminal width of s: one column per rune, two for the
// status icons.
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		n++
		if strings.ContainsRune(iconOK+iconDiffers+iconFailed, r) {
			n++
		}
	}
	return n
}
