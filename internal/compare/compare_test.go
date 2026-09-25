package compare

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kmpoltorak/dns-consistency-checker/internal/dnsclient"
	"github.com/kmpoltorak/dns-consistency-checker/internal/normalize"
)

// res builds a result. values are "VALUE" or "VALUE@TTL".
func res(name string, status dnsclient.Status, values ...string) dnsclient.Result {
	r, err := dnsclient.ParseResolver(name, fmt.Sprintf("192.0.2.%d", len(name)+1))
	if err != nil {
		panic(err)
	}
	out := dnsclient.Result{Resolver: r, Status: status, Attempts: 1}
	var recs []normalize.Record
	for _, v := range values {
		val, ttlStr, _ := strings.Cut(v, "@")
		ttl, err := strconv.ParseUint(cmp.Or(ttlStr, "300"), 10, 32)
		if err != nil {
			panic(err)
		}
		recs = append(recs, normalize.Record{Type: "A", Value: val, TTL: uint32(ttl)})
	}
	out.Records = normalize.RRset(recs)
	if !status.Usable() {
		out.Error = "failure"
	}
	return out
}

const (
	ok   = dnsclient.StatusNoError
	nx   = dnsclient.StatusNXDomain
	sf   = dnsclient.StatusServFail
	tout = dnsclient.StatusTimeout
)

func issueTypes(rep Report) []IssueType {
	var out []IssueType
	for _, i := range rep.Issues {
		out = append(out, i.Type)
	}
	return out
}

func TestOverallStatus(t *testing.T) {
	tests := []struct {
		name    string
		results []dnsclient.Result
		opts    Options
		want    Overall
	}{
		{"same A", []dnsclient.Result{res("a", ok, "10.0.0.1", "10.0.0.2"), res("bb", ok, "10.0.0.1", "10.0.0.2")}, Options{}, Consistent},
		{"different order", []dnsclient.Result{res("a", ok, "10.0.0.1", "10.0.0.2"), res("bb", ok, "10.0.0.2", "10.0.0.1")}, Options{}, Consistent},
		{"different RRset", []dnsclient.Result{res("a", ok, "10.0.0.1", "10.0.0.2"), res("bb", ok, "10.0.0.1")}, Options{}, Inconsistent},
		{"timeout", []dnsclient.Result{res("a", ok, "10.0.0.1"), res("bb", ok, "10.0.0.1"), res("ccc", ok, "10.0.0.1"), res("dddd", tout)}, Options{}, PartialFailure},
		{"servfail", []dnsclient.Result{res("a", ok, "10.0.0.1"), res("bb", sf)}, Options{}, PartialFailure},
		{"refused", []dnsclient.Result{res("a", ok, "10.0.0.1"), res("bb", dnsclient.StatusRefused)}, Options{}, PartialFailure},
		{"NOERROR vs NXDOMAIN", []dnsclient.Result{res("a", ok, "10.0.0.1"), res("bb", nx)}, Options{}, Inconsistent},
		{"all NXDOMAIN", []dnsclient.Result{res("a", nx), res("bb", nx)}, Options{}, Consistent},
		{"all failed", []dnsclient.Result{res("a", sf), res("bb", tout)}, Options{}, TotalFailure},
		{"none", nil, Options{}, TotalFailure},
		{"TTL ignored", []dnsclient.Result{res("a", ok, "10.0.0.1@300"), res("bb", ok, "10.0.0.1@120")}, Options{}, Consistent},
		{"TTL compared", []dnsclient.Result{res("a", ok, "10.0.0.1@300"), res("bb", ok, "10.0.0.1@120")}, Options{CompareTTL: true}, Inconsistent},
		{"inconsistent beats failure", []dnsclient.Result{res("a", ok, "10.0.0.1"), res("bb", ok, "10.0.0.2"), res("ccc", tout)}, Options{}, Inconsistent},
		{"expected match", []dnsclient.Result{res("a", ok, "10.0.0.1")}, Options{Expected: []normalize.Record{{Value: "10.0.0.1"}}}, Consistent},
		{"expected mismatch", []dnsclient.Result{res("a", ok, "10.0.0.1")}, Options{Expected: []normalize.Record{{Value: "10.0.0.2"}}}, Inconsistent},
		{"expected with failure", []dnsclient.Result{res("a", ok, "10.0.0.1"), res("bb", sf)}, Options{Expected: []normalize.Record{{Value: "10.0.0.1"}}}, PartialFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Analyze(tt.results, tt.opts).Status; got != tt.want {
				t.Fatalf("status = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGroupingMajorityOutliers(t *testing.T) {
	var results []dnsclient.Result
	for i := range 8 {
		results = append(results, res(fmt.Sprint("maj", i), ok, "10.0.0.11", "10.0.0.10"))
	}
	results = slices.Insert(results, 3, res("x", ok, "10.0.0.20"))
	results = append(results, res("y", ok, "10.0.0.20"), res("z", nx), res("f", sf))

	rep := Analyze(results, Options{})
	if rep.Status != Inconsistent || rep.Successful != 11 || rep.Failed != 1 {
		t.Fatalf("status=%s ok=%d failed=%d", rep.Status, rep.Successful, rep.Failed)
	}
	if len(rep.Groups) != 3 {
		t.Fatalf("groups = %d", len(rep.Groups))
	}
	maj := rep.MajorityGroup()
	if maj == nil || maj.ID != 1 || len(maj.Members) != 8 || maj.Records[0].Value != "10.0.0.10" {
		t.Fatalf("majority = %+v", maj)
	}
	if rep.Groups[1].Status != ok || len(rep.Groups[1].Members) != 2 || rep.Groups[2].Status != nx {
		t.Fatalf("group order wrong: %+v", rep.Groups)
	}
	if want := []int{3, 9, 10}; !slices.Equal(rep.Outliers, want) {
		t.Fatalf("outliers = %v, want %v", rep.Outliers, want)
	}
	if rep.GroupOf(3) != 2 || rep.GroupOf(11) != 0 || rep.GroupOf(0) != 1 {
		t.Fatal("GroupOf wrong")
	}
	want := []IssueType{IssueDifferentRRset, IssueDifferentRRset, IssueDifferentRcode, IssueServFail}
	if got := issueTypes(rep); !slices.Equal(got, want) {
		t.Fatalf("issues = %v, want %v", got, want)
	}
	if rep.Issues[2].Message != "z (192.0.2.2) returned NXDOMAIN while the majority returned NOERROR" {
		t.Fatalf("message = %q", rep.Issues[2].Message)
	}
}

func TestNoMajorityOnTie(t *testing.T) {
	rep := Analyze([]dnsclient.Result{res("a", ok, "10.0.0.1"), res("bb", ok, "10.0.0.2")}, Options{})
	if rep.Majority != -1 || rep.MajorityGroup() != nil || len(rep.Outliers) != 0 {
		t.Fatalf("majority=%d outliers=%v", rep.Majority, rep.Outliers)
	}
	if got := issueTypes(rep); !slices.Equal(got, []IssueType{IssueNoMajority}) {
		t.Fatalf("issues = %v", got)
	}
}

func TestTTLDifferenceIsInfo(t *testing.T) {
	rep := Analyze([]dnsclient.Result{
		res("a", ok, "10.0.0.1@300", "10.0.0.2@60"),
		res("bb", ok, "10.0.0.1@120", "10.0.0.2@60"),
	}, Options{})
	if len(rep.Issues) != 1 || rep.Issues[0].Type != IssueTTLDifference || rep.Issues[0].Severity != SeverityInfo {
		t.Fatalf("issues = %+v", rep.Issues)
	}
	if want := "TTL values differ within response group 1: TTL 120-300s on 1 of 2 records (per-record TTLs are in the structured output)"; rep.Issues[0].Message != want {
		t.Fatalf("message = %q", rep.Issues[0].Message)
	}

	strict := Analyze(rep.Results, Options{CompareTTL: true})
	if len(strict.Groups) != 2 || strict.Majority != -1 {
		t.Fatalf("groups=%d majority=%d", len(strict.Groups), strict.Majority)
	}
}

func TestTTLOnlyOutlierMessage(t *testing.T) {
	rep := Analyze([]dnsclient.Result{
		res("a", ok, "10.0.0.1@300"), res("bb", ok, "10.0.0.1@300"), res("ccc", ok, "10.0.0.1@5"),
	}, Options{CompareTTL: true})
	if rep.Status != Inconsistent || len(rep.Issues) != 1 || rep.Issues[0].Message != "ccc (192.0.2.4) returned different TTLs than the majority" {
		t.Fatalf("issues = %+v", rep.Issues)
	}
}

func TestCNAMEChainParticipates(t *testing.T) {
	a := res("a", ok, "10.0.0.1")
	a.CNAMEChain = []string{"x.example."}
	b := res("bb", ok, "10.0.0.1")
	b.CNAMEChain = []string{"y.example."}
	c := res("ccc", ok, "10.0.0.1")
	c.CNAMEChain = []string{"x.example."}
	rep := Analyze([]dnsclient.Result{a, b, c}, Options{})
	if rep.Status != Inconsistent || rep.Issues[0].Message != "bb (192.0.2.3) returned a different CNAME chain than the majority" {
		t.Fatalf("status=%s issues=%+v", rep.Status, rep.Issues)
	}
}

func TestExpectedMatching(t *testing.T) {
	results := []dnsclient.Result{
		res("a", ok, "10.20.30.41", "10.20.30.40"),
		res("bb", ok, "10.20.30.40"),
		res("ccc", nx),
		res("dddd", tout),
	}
	rep := Analyze(results, Options{Expected: []normalize.Record{{Value: "10.20.30.40"}, {Value: "10.20.30.41"}}})
	e := rep.Expected
	if !slices.Equal(e.Matching, []int{0}) || !slices.Equal(e.NonMatching, []int{1, 2}) || !slices.Equal(e.Failed, []int{3}) {
		t.Fatalf("expected = %+v", e)
	}
	if n := slices.Collect(func(yield func(IssueType) bool) {
		for _, i := range rep.Issues {
			if i.Type == IssueExpectedMismatch && !yield(i.Type) {
				return
			}
		}
	}); len(n) != 2 {
		t.Fatalf("expected mismatch issues = %d", len(n))
	}
}

func TestFailureIssueTypes(t *testing.T) {
	results := []dnsclient.Result{
		res("a", ok, "10.0.0.1"),
		res("b", tout), res("c", dnsclient.StatusNetworkError), res("d", dnsclient.StatusProtocolError),
		res("e", sf), res("f", dnsclient.StatusRefused), res("g", dnsclient.StatusFormErr),
		res("h", dnsclient.StatusNotImp), res("i", dnsclient.Status("YXDOMAIN")),
	}
	want := []IssueType{IssueTimeout, IssueNetworkError, IssueProtocolError, IssueServFail, IssueRefused,
		IssueFormErr, IssueNotImp, IssueRcodeError}
	rep := Analyze(results, Options{})
	if got := issueTypes(rep); !slices.Equal(got, want) {
		t.Fatalf("issues = %v", got)
	}
	if rep.Status != PartialFailure {
		t.Fatalf("status = %s", rep.Status)
	}
}

func TestTruncatedWarning(t *testing.T) {
	r := res("a", ok, "10.0.0.1")
	r.Flags.Truncated = true
	rep := Analyze([]dnsclient.Result{r}, Options{})
	if rep.Status != Consistent || len(rep.Issues) != 1 || rep.Issues[0].Type != IssueTruncated {
		t.Fatalf("got %s %+v", rep.Status, rep.Issues)
	}
}

func TestDeterminism(t *testing.T) {
	results := []dnsclient.Result{
		res("a", ok, "10.0.0.2"), res("bb", ok, "10.0.0.1"), res("ccc", nx), res("dddd", ok, "10.0.0.3"),
	}
	first := Analyze(results, Options{})
	for range 50 {
		if again := Analyze(results, Options{}); !reflect.DeepEqual(first, again) {
			t.Fatal("analysis is not deterministic")
		}
	}
	// Groups of equal size are ordered by comparison key, independent of input order.
	reversed := slices.Clone(results)
	slices.Reverse(reversed)
	rev := Analyze(reversed, Options{})
	for i := range first.Groups {
		if !sameValues(first.Groups[i].Records, rev.Groups[i].Records) {
			t.Fatal("group order depends on input order")
		}
	}
}

func BenchmarkKey(b *testing.B) {
	r := res("a", ok, "10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4")
	for b.Loop() {
		_ = Key(r, false)
	}
}

func BenchmarkAnalyze100(b *testing.B) {
	var results []dnsclient.Result
	for i := range 100 {
		switch i % 10 {
		case 0:
			results = append(results, res(fmt.Sprint(i), tout))
		case 1:
			results = append(results, res(fmt.Sprint(i), ok, "10.0.0.9"))
		default:
			results = append(results, res(fmt.Sprint(i), ok, "10.0.0.1", "10.0.0.2", "10.0.0.3"))
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = Analyze(results, Options{})
	}
}

func TestDuplicateNotes(t *testing.T) {
	rep := Analyze([]dnsclient.Result{res("a", ok, "10.0.0.1")}, Options{Duplicates: []string{"duplicate resolver x ignored"}})
	if rep.Status != Consistent || len(rep.Issues) != 1 || rep.Issues[0].Type != IssueDuplicate ||
		rep.Issues[0].Severity != SeverityInfo || rep.Issues[0].Message != "duplicate resolver x ignored" {
		t.Fatalf("got %s %+v", rep.Status, rep.Issues)
	}
}

func TestInfoIssues(t *testing.T) {
	r := res("a", ok, "10.0.0.1")
	r.ProtocolInitial, r.ProtocolFinal, r.IgnoredRecords = "udp", "tcp", 2
	rep := Analyze([]dnsclient.Result{r}, Options{})
	if got := issueTypes(rep); rep.Status != Consistent || !slices.Equal(got, []IssueType{IssueTCPFallback, IssueIgnoredRecords}) {
		t.Fatalf("got %s %v", rep.Status, got)
	}
	for _, is := range rep.Issues {
		if is.Severity != SeverityInfo {
			t.Fatalf("severity %s", is.Severity)
		}
	}
}
