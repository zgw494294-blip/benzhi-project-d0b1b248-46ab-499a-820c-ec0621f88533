package sample

import (
	"time"

	"arcproof/internal/app"
	"arcproof/internal/domain"
)

func Rule() app.CreateRuleSetInput {
	return app.CreateRuleSetInput{Name: "压力设备焊接工艺规则", Edition: "2026-A", Variables: []domain.VariableRule{
		{Name: "base_materials", Class: domain.ClassEssential, Clause: "4.1", Values: []string{"P1", "P2", "P3"}},
		{Name: "process", Class: domain.ClassEssential, Clause: "4.2", Values: []string{"GTAW", "SMAW"}},
		{Name: "joint_type", Class: domain.ClassEssential, Clause: "4.3", Values: []string{"BUTT", "FILLET"}},
		{Name: "position", Class: domain.ClassNonEssential, Clause: "4.4", Values: []string{"1G", "2G", "5G"}},
		{Name: "thickness", Class: domain.ClassEssential, Clause: "5.1", Numeric: &domain.Interval{Min: domain.MustFixed("2"), Max: domain.MustFixed("40"), Unit: "mm"}},
		{Name: "diameter", Class: domain.ClassEssential, Clause: "5.2", Numeric: &domain.Interval{Min: domain.MustFixed("25"), Max: domain.MustFixed("1000"), Unit: "mm"}},
		{Name: "filler", Class: domain.ClassEssential, Clause: "6.1", Values: []string{"ER70S-6", "E7018"}},
		{Name: "preheat", Class: domain.ClassEssential, Clause: "7.1", Numeric: &domain.Interval{Min: domain.MustFixed("80"), Max: domain.MustFixed("250"), Unit: "C"}},
		{Name: "heat_input", Class: domain.ClassSupplementary, Clause: "7.2", Numeric: &domain.Interval{Min: domain.MustFixed("0.5"), Max: domain.MustFixed("2.5"), Unit: "kJ/mm"}, RequiredWhen: "LOW_TEMP"},
		{Name: "pwht", Class: domain.ClassEssential, Clause: "8.1", Values: []string{"NONE", "620C-2H"}},
		{Name: "service", Class: domain.ClassSupplementary, Clause: "9.1", Values: []string{"NORMAL", "LOW_TEMP"}, RequiredWhen: "LOW_TEMP"},
	}, RequiredTests: []domain.RequiredTest{{Type: "TENSILE", MinResult: domain.MustFixed("450"), Unit: "MPa"}, {Type: "BEND", MinResult: domain.MustFixed("1"), Unit: "pass"}, {Type: "IMPACT", MinResult: domain.MustFixed("27"), Unit: "J", RequiredWhen: "LOW_TEMP"}}}
}
func Variables() domain.ProcedureVariables {
	return domain.ProcedureVariables{BaseMaterials: []string{"P1", "P1"}, Process: "GTAW", JointType: "BUTT", Position: "1G", Thickness: domain.MustFixed("12"), Diameter: domain.MustFixed("200"), Filler: "ER70S-6", Preheat: domain.MustFixed("100"), HeatInput: domain.MustFixed("1.2"), PWHT: "NONE", Service: "NORMAL"}
}
func Evidence(expected int) []app.AddEvidenceInput {
	return []app.AddEvidenceInput{{ExpectedVersion: expected, Type: "TENSILE", Result: domain.MustFixed("510"), Unit: "MPa", Passed: true, Source: "LAB-001", TestedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}, {ExpectedVersion: expected + 1, Type: "BEND", Result: domain.MustFixed("1"), Unit: "pass", Passed: true, Source: "LAB-002", TestedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}}
}
