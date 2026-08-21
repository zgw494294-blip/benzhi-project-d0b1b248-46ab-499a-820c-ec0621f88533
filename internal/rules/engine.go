package rules

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"arcproof/internal/domain"
)

var allowedClasses = map[domain.VariableClass]bool{domain.ClassNonEssential: true, domain.ClassEssential: true, domain.ClassSupplementary: true}
var knownVariables = map[string]bool{"base_materials": true, "process": true, "joint_type": true, "position": true, "thickness": true, "diameter": true, "filler": true, "preheat": true, "heat_input": true, "pwht": true, "service": true}

func ValidateRuleSet(r domain.RuleSet) []string {
	var problems []string
	if strings.TrimSpace(r.Name) == "" {
		problems = append(problems, "规则名称不能为空")
	}
	if strings.TrimSpace(r.Edition) == "" {
		problems = append(problems, "规则版次不能为空")
	}
	seen := map[string]bool{}
	for i, v := range r.Variables {
		if !knownVariables[v.Name] {
			problems = append(problems, fmt.Sprintf("变量[%d]名称未知: %s", i, v.Name))
		}
		if seen[v.Name] {
			problems = append(problems, "变量分类重复: "+v.Name)
		}
		seen[v.Name] = true
		if !allowedClasses[v.Class] {
			problems = append(problems, "变量分类非法: "+v.Name)
		}
		if strings.TrimSpace(v.Clause) == "" {
			problems = append(problems, "规则条款不能为空: "+v.Name)
		}
		if v.Numeric == nil && len(v.Values) == 0 {
			problems = append(problems, "变量必须定义区间或离散集合: "+v.Name)
		}
		if v.Numeric != nil && !v.Numeric.Valid() {
			problems = append(problems, "变量区间非法: "+v.Name)
		}
		if v.Numeric != nil && len(v.Values) > 0 {
			problems = append(problems, "变量不能同时定义区间和集合: "+v.Name)
		}
		if len(v.Values) > 0 {
			values := map[string]bool{}
			for _, x := range v.Values {
				n := strings.TrimSpace(strings.ToUpper(x))
				if n == "" {
					problems = append(problems, "变量集合含空值: "+v.Name)
				}
				if values[n] {
					problems = append(problems, "变量集合含重复值: "+v.Name)
				}
				values[n] = true
			}
		}
	}
	for _, required := range []string{"base_materials", "process", "joint_type", "thickness", "diameter", "filler", "preheat", "pwht"} {
		if !seen[required] {
			problems = append(problems, "缺少必要变量规则: "+required)
		}
	}
	tests := map[string]bool{}
	for _, t := range r.RequiredTests {
		if strings.TrimSpace(t.Type) == "" || strings.TrimSpace(t.Unit) == "" {
			problems = append(problems, "必需试验类型和单位不能为空")
		}
		if tests[t.Type] {
			problems = append(problems, "必需试验重复: "+t.Type)
		}
		tests[t.Type] = true
	}
	if len(r.RequiredTests) == 0 {
		problems = append(problems, "至少需要一种必需试验")
	}
	sort.Strings(problems)
	return problems
}

func conditionApplies(condition, service string) bool {
	condition = strings.TrimSpace(strings.ToUpper(condition))
	return condition == "" || condition == strings.ToUpper(service)
}

func EvaluateQualification(rule domain.RuleSet, q domain.QualificationRecord, at time.Time) ([]string, bool) {
	var reasons []string
	for _, required := range rule.RequiredTests {
		if !conditionApplies(required.RequiredWhen, q.Variables.Service) {
			continue
		}
		found := false
		for _, e := range q.Evidence {
			if strings.EqualFold(e.Type, required.Type) && e.Active(at) {
				found = true
				if !e.Passed || e.Unit != required.Unit || e.Result < required.MinResult {
					reasons = append(reasons, fmt.Sprintf("试验 %s 未达到 %s %s", required.Type, required.MinResult.String(), required.Unit))
				}
				break
			}
		}
		if !found {
			reasons = append(reasons, "缺少有效试验: "+required.Type)
		}
	}
	return reasons, len(reasons) == 0
}

func DeriveCoverage(rule domain.RuleSet, vars domain.ProcedureVariables) ([]domain.CoverageRange, []string) {
	input := domain.Digest(vars)
	var coverage []domain.CoverageRange
	var gaps []string
	for _, r := range rule.Variables {
		if !conditionApplies(r.RequiredWhen, vars.Service) {
			continue
		}
		c := domain.CoverageRange{Variable: r.Name, Clause: r.Clause, InputDigest: input}
		switch r.Name {
		case "base_materials":
			if allowedPair(vars.BaseMaterials, r.Values) {
				c.Values = domain.CanonicalStrings(vars.BaseMaterials)
			} else {
				gaps = append(gaps, "母材组合不在规则范围")
			}
		case "process":
			c.Values = deriveValue(vars.Process, r, &gaps)
		case "joint_type":
			c.Values = deriveValue(vars.JointType, r, &gaps)
		case "position":
			c.Values = deriveValue(vars.Position, r, &gaps)
		case "filler":
			c.Values = deriveValue(vars.Filler, r, &gaps)
		case "pwht":
			c.Values = deriveValue(vars.PWHT, r, &gaps)
		case "service":
			c.Values = deriveValue(vars.Service, r, &gaps)
		case "thickness":
			c.Numeric = deriveNumeric(vars.Thickness, r, &gaps)
		case "diameter":
			c.Numeric = deriveNumeric(vars.Diameter, r, &gaps)
		case "preheat":
			c.Numeric = deriveNumeric(vars.Preheat, r, &gaps)
		case "heat_input":
			c.Numeric = deriveNumeric(vars.HeatInput, r, &gaps)
		}
		if c.Numeric != nil || len(c.Values) > 0 {
			coverage = append(coverage, c)
		}
	}
	sort.Slice(coverage, func(i, j int) bool { return coverage[i].Variable < coverage[j].Variable })
	sort.Strings(gaps)
	return coverage, gaps
}

// IntersectCoverage 合并多条来源评定的共同覆盖范围，并保留全部推导依据。
func IntersectCoverage(sets ...[]domain.CoverageRange) ([]domain.CoverageRange, []string) {
	if len(sets) == 0 {
		return nil, []string{"没有可用于求交的来源评定范围"}
	}
	current := make(map[string]domain.CoverageRange, len(sets[0]))
	for _, item := range sets[0] {
		current[item.Variable] = cloneCoverage(item)
	}
	var gaps []string
	for sourceIndex := 1; sourceIndex < len(sets); sourceIndex++ {
		next := make(map[string]domain.CoverageRange, len(sets[sourceIndex]))
		for _, item := range sets[sourceIndex] {
			next[item.Variable] = item
		}
		for variable, existing := range current {
			candidate, ok := next[variable]
			if !ok {
				gaps = append(gaps, fmt.Sprintf("第 %d 条来源评定缺少变量 %s 的范围", sourceIndex+1, variable))
				delete(current, variable)
				continue
			}
			merged := existing
			merged.Clause = mergeClauses(existing.Clause, candidate.Clause)
			merged.InputDigest = domain.Digest([]string{existing.InputDigest, candidate.InputDigest})
			switch {
			case existing.Numeric != nil && candidate.Numeric != nil:
				intersection, exists := existing.Numeric.Intersect(*candidate.Numeric)
				if !exists {
					gaps = append(gaps, "来源评定的数值范围无共同交集: "+variable)
					delete(current, variable)
					continue
				}
				merged.Numeric = &intersection
			case len(existing.Values) > 0 && len(candidate.Values) > 0:
				merged.Values = intersectValues(existing.Values, candidate.Values)
				if len(merged.Values) == 0 {
					gaps = append(gaps, "来源评定的离散范围无共同交集: "+variable)
					delete(current, variable)
					continue
				}
			default:
				gaps = append(gaps, "来源评定的范围类型不一致: "+variable)
				delete(current, variable)
				continue
			}
			current[variable] = merged
		}
		for variable := range next {
			if _, ok := current[variable]; !ok && !coverageSetContains(sets[0], variable) {
				gaps = append(gaps, fmt.Sprintf("首条来源评定缺少变量 %s 的范围", variable))
			}
		}
	}
	result := make([]domain.CoverageRange, 0, len(current))
	for _, item := range current {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Variable < result[j].Variable })
	sort.Strings(gaps)
	return result, unique(gaps)
}

func cloneCoverage(in domain.CoverageRange) domain.CoverageRange {
	out := in
	out.Values = append([]string(nil), in.Values...)
	if in.Numeric != nil {
		copy := *in.Numeric
		out.Numeric = &copy
	}
	return out
}
func coverageSetContains(set []domain.CoverageRange, name string) bool {
	for _, item := range set {
		if item.Variable == name {
			return true
		}
	}
	return false
}
func intersectValues(left, right []string) []string {
	rightSet := map[string]bool{}
	for _, v := range right {
		rightSet[strings.ToUpper(v)] = true
	}
	var result []string
	for _, v := range left {
		normalized := strings.ToUpper(v)
		if rightSet[normalized] {
			result = append(result, normalized)
		}
	}
	sort.Strings(result)
	return unique(result)
}
func mergeClauses(left, right string) string {
	if left == right {
		return left
	}
	parts := []string{left, right}
	sort.Strings(parts)
	return strings.Join(unique(parts), " + ")
}
func unique(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
func allowedPair(actual, allowed []string) bool {
	if len(actual) != 2 {
		return false
	}
	set := map[string]bool{}
	for _, x := range allowed {
		set[strings.ToUpper(x)] = true
	}
	return set[strings.ToUpper(actual[0])] && set[strings.ToUpper(actual[1])]
}
func deriveValue(actual string, r domain.VariableRule, gaps *[]string) []string {
	for _, x := range r.Values {
		if strings.EqualFold(x, actual) {
			return []string{strings.ToUpper(actual)}
		}
	}
	*gaps = append(*gaps, fmt.Sprintf("%s=%s 不在规则集合", r.Name, actual))
	return nil
}
func deriveNumeric(actual domain.Fixed, r domain.VariableRule, gaps *[]string) *domain.Interval {
	if r.Numeric == nil || !r.Numeric.Contains(actual) {
		*gaps = append(*gaps, fmt.Sprintf("%s=%s 越出规则区间", r.Name, actual.String()))
		return nil
	}
	v := *r.Numeric
	return &v
}

func Assess(coverage []domain.CoverageRange, requirement domain.ProcedureVariables, rule domain.RuleSet) (string, []domain.Difference, []string) {
	var diffs []domain.Difference
	var conditions []string
	byName := map[string]domain.CoverageRange{}
	for _, c := range coverage {
		byName[c.Variable] = c
	}
	for _, r := range rule.Variables {
		if !conditionApplies(r.RequiredWhen, requirement.Service) {
			continue
		}
		c, ok := byName[r.Name]
		if !ok {
			diffs = append(diffs, difference(r, "存在覆盖范围", "缺失", "BLOCKING", "缺少派生覆盖范围"))
			continue
		}
		actualText := ""
		match := true
		switch r.Name {
		case "base_materials":
			actualText = strings.Join(requirement.BaseMaterials, ",")
			match = domain.EqualStrings(c.Values, requirement.BaseMaterials)
		case "process":
			actualText = requirement.Process
			match = contains(c.Values, actualText)
		case "joint_type":
			actualText = requirement.JointType
			match = contains(c.Values, actualText)
		case "position":
			actualText = requirement.Position
			match = contains(c.Values, actualText)
		case "filler":
			actualText = requirement.Filler
			match = contains(c.Values, actualText)
		case "pwht":
			actualText = requirement.PWHT
			match = contains(c.Values, actualText)
		case "service":
			actualText = requirement.Service
			match = contains(c.Values, actualText)
		case "thickness":
			actualText = requirement.Thickness.String()
			match = c.Numeric != nil && c.Numeric.Contains(requirement.Thickness)
		case "diameter":
			actualText = requirement.Diameter.String()
			match = c.Numeric != nil && c.Numeric.Contains(requirement.Diameter)
		case "preheat":
			actualText = requirement.Preheat.String()
			match = c.Numeric != nil && c.Numeric.Contains(requirement.Preheat)
		case "heat_input":
			actualText = requirement.HeatInput.String()
			match = c.Numeric != nil && c.Numeric.Contains(requirement.HeatInput)
		}
		if !match {
			severity := "BLOCKING"
			if r.Class == domain.ClassNonEssential {
				severity = "CONDITION"
				conditions = append(conditions, fmt.Sprintf("生产前确认 %s 的偏差已由焊接工程师接受", r.Name))
			}
			diffs = append(diffs, difference(r, coverageText(c), actualText, severity, "生产要求未被规程覆盖"))
		}
	}
	if len(diffs) == 0 {
		return "APPLICABLE", diffs, conditions
	}
	if len(conditions) == len(diffs) {
		return "CONDITIONAL", diffs, conditions
	}
	return "NOT_APPLICABLE", diffs, conditions
}
func difference(r domain.VariableRule, expected, actual, severity, msg string) domain.Difference {
	return domain.Difference{Variable: r.Name, Expected: expected, Actual: actual, Class: r.Class, Clause: r.Clause, Severity: severity, Message: msg}
}
func coverageText(c domain.CoverageRange) string {
	if c.Numeric != nil {
		return c.Numeric.Min.String() + ".." + c.Numeric.Max.String() + " " + c.Numeric.Unit
	}
	return strings.Join(c.Values, ",")
}
func contains(values []string, v string) bool {
	for _, x := range values {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func Compare(rule domain.RuleSet, from, to domain.ProcedureVariables) ([]domain.Difference, []string) {
	var diffs []domain.Difference
	var blocking []string
	for _, r := range rule.Variables {
		a, b := variableText(r.Name, from), variableText(r.Name, to)
		if a == b {
			continue
		}
		severity := "INFO"
		if r.Class == domain.ClassEssential {
			severity = "REQUALIFICATION"
			blocking = append(blocking, "重要变量变更: "+r.Name)
		} else if r.Class == domain.ClassSupplementary && conditionApplies(r.RequiredWhen, to.Service) {
			severity = "SUPPLEMENTARY_TEST"
			blocking = append(blocking, "附加重要变量变更: "+r.Name)
		}
		diffs = append(diffs, difference(r, a, b, severity, "规程修订变量发生变化"))
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Variable < diffs[j].Variable })
	sort.Strings(blocking)
	return diffs, blocking
}
func variableText(name string, v domain.ProcedureVariables) string {
	switch name {
	case "base_materials":
		return strings.Join(domain.CanonicalStrings(v.BaseMaterials), ",")
	case "process":
		return v.Process
	case "joint_type":
		return v.JointType
	case "position":
		return v.Position
	case "thickness":
		return v.Thickness.String()
	case "diameter":
		return v.Diameter.String()
	case "filler":
		return v.Filler
	case "preheat":
		return v.Preheat.String()
	case "heat_input":
		return v.HeatInput.String()
	case "pwht":
		return v.PWHT
	case "service":
		return v.Service
	}
	return ""
}
