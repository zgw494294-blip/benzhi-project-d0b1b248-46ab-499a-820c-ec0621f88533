package rules

import (
	"testing"

	"arcproof/internal/domain"
)

func TestIntersectCoverage(t *testing.T) {
	left := []domain.CoverageRange{{Variable: "thickness", Numeric: &domain.Interval{Min: domain.MustFixed("2"), Max: domain.MustFixed("40"), Unit: "mm"}, Clause: "A", InputDigest: "1"}, {Variable: "process", Values: []string{"GTAW", "SMAW"}, Clause: "B", InputDigest: "1"}}
	right := []domain.CoverageRange{{Variable: "thickness", Numeric: &domain.Interval{Min: domain.MustFixed("10"), Max: domain.MustFixed("50"), Unit: "mm"}, Clause: "C", InputDigest: "2"}, {Variable: "process", Values: []string{"GTAW"}, Clause: "B", InputDigest: "2"}}
	merged, gaps := IntersectCoverage(left, right)
	if len(gaps) != 0 || len(merged) != 2 {
		t.Fatalf("求交异常: %+v %+v", merged, gaps)
	}
	byName := map[string]domain.CoverageRange{}
	for _, item := range merged {
		byName[item.Variable] = item
	}
	if byName["thickness"].Numeric.Min != domain.MustFixed("10") || byName["thickness"].Numeric.Max != domain.MustFixed("40") {
		t.Fatal("数值交集错误")
	}
	if len(byName["process"].Values) != 1 || byName["process"].Values[0] != "GTAW" {
		t.Fatal("离散交集错误")
	}
}
