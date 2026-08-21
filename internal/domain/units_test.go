package domain

import "testing"

func TestNormalizeMeasurement(t *testing.T) {
	v, u, err := NormalizeMeasurement("TENSILE", MustFixed("510000"), "kPa")
	if err != nil || v != MustFixed("510") || u != "MPa" {
		t.Fatalf("换算错误: %s %s %v", v.String(), u, err)
	}
	v, u, err = NormalizeMeasurement("IMPACT", MustFixed("0.027"), "kJ")
	if err != nil || v != MustFixed("27") || u != "J" {
		t.Fatalf("换算错误: %s %s %v", v.String(), u, err)
	}
}
func TestClosedInterval(t *testing.T) {
	i := Interval{Min: MustFixed("2"), Max: MustFixed("40"), Unit: "mm"}
	if !i.Contains(MustFixed("2")) || !i.Contains(MustFixed("40")) || i.Contains(MustFixed("40.001")) {
		t.Fatal("闭区间边界语义错误")
	}
}
