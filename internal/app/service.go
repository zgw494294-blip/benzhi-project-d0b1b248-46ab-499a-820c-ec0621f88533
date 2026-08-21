package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"arcproof/internal/clock"
	"arcproof/internal/domain"
	"arcproof/internal/rules"
	"arcproof/internal/store"
)

type Service struct {
	repo  *store.Repository
	clock clock.Clock
}

func New(repo *store.Repository, c clock.Clock) *Service {
	if c == nil {
		c = clock.Real{}
	}
	return &Service{repo: repo, clock: c}
}
func (s *Service) Repository() *store.Repository { return s.repo }

type CreateRuleSetInput struct {
	Name          string                `json:"name"`
	Edition       string                `json:"edition"`
	Variables     []domain.VariableRule `json:"variables"`
	RequiredTests []domain.RequiredTest `json:"required_tests"`
}
type UpdateRuleSetInput struct {
	ExpectedVersion int                   `json:"expected_version"`
	Name            string                `json:"name"`
	Edition         string                `json:"edition"`
	Variables       []domain.VariableRule `json:"variables"`
	RequiredTests   []domain.RequiredTest `json:"required_tests"`
}

func (s *Service) CreateRuleSet(actor string, in CreateRuleSetInput) (domain.RuleSet, error) {
	id := newID("rule")
	r := domain.RuleSet{ID: id, Name: strings.TrimSpace(in.Name), Edition: strings.TrimSpace(in.Edition), Status: domain.RuleDraft, Version: 1, Variables: in.Variables, RequiredTests: in.RequiredTests}
	r.Digest = domain.Digest(struct {
		Name, Edition string
		Variables     []domain.VariableRule
		Tests         []domain.RequiredTest
	}{r.Name, r.Edition, r.Variables, r.RequiredTests})
	err := s.repo.Update(actor, "RULE_SET_CREATED", "rule_set", id, func(st *domain.State) error { st.Rules[id] = r; return nil })
	return r, err
}
func (s *Service) UpdateRuleSet(actor, id string, in UpdateRuleSetInput) (domain.RuleSet, error) {
	var out domain.RuleSet
	err := s.repo.Update(actor, "RULE_SET_UPDATED", "rule_set", id, func(st *domain.State) error {
		r, ok := st.Rules[id]
		if !ok {
			return domain.NotFound("规则", id)
		}
		if r.Status != domain.RuleDraft {
			return domain.Conflict("IMMUTABLE_RULE_SET", "已发布规则不可修改")
		}
		if r.Version != in.ExpectedVersion {
			return versionConflict(r.Version)
		}
		r.Name = strings.TrimSpace(in.Name)
		r.Edition = strings.TrimSpace(in.Edition)
		r.Variables = in.Variables
		r.RequiredTests = in.RequiredTests
		r.Version++
		r.Digest = domain.Digest(in)
		st.Rules[id] = r
		out = r
		return nil
	})
	return out, err
}
func (s *Service) PublishRuleSet(actor, id string, expected int) (domain.RuleSet, error) {
	var out domain.RuleSet
	now := s.clock.Now()
	err := s.repo.Update(actor, "RULE_SET_PUBLISHED", "rule_set", id, func(st *domain.State) error {
		r, ok := st.Rules[id]
		if !ok {
			return domain.NotFound("规则", id)
		}
		if r.Status != domain.RuleDraft {
			return domain.Conflict("RULE_STATE_CONFLICT", "只有草稿规则可以发布")
		}
		if r.Version != expected {
			return versionConflict(r.Version)
		}
		if problems := rules.ValidateRuleSet(r); len(problems) > 0 {
			return domain.Invalid("规则发布校验失败", problems)
		}
		for oldID, old := range st.Rules {
			if old.Status == domain.RulePublished {
				old.Status = domain.RuleSuperseded
				old.Version++
				st.Rules[oldID] = old
				enqueueAffected(st, "RULE_UPGRADE", "rule_set", oldID, id, now)
				for revisionID, revision := range st.Revisions {
					if revision.RuleSetID != oldID {
						continue
					}
					enqueueAffected(st, "RULE_UPGRADE", "revision", revisionID, id, now)
					markAssessmentsStale(st, revisionID, "规则已升级，等待按新版本复核")
				}
				for reviewID, review := range st.ChangeReviews {
					if review.RuleSetID == oldID && (review.Status == domain.ReviewOpen || review.Status == domain.ReviewReady) {
						enqueueAffected(st, "RULE_UPGRADE", "change_review", reviewID, id, now)
					}
				}
			}
		}
		r.Status = domain.RulePublished
		r.Version++
		r.PublishedAt = &now
		r.Digest = domain.Digest(r)
		st.Rules[id] = r
		out = r
		return nil
	})
	return out, err
}
func (s *Service) GetRuleSet(id string) (domain.RuleSet, error) {
	var out domain.RuleSet
	err := s.repo.View(func(st domain.State) error {
		r, ok := st.Rules[id]
		if !ok {
			return domain.NotFound("规则", id)
		}
		out = r
		return nil
	})
	return out, err
}

type CreateQualificationInput struct {
	RuleSetID string                    `json:"rule_set_id"`
	Variables domain.ProcedureVariables `json:"variables"`
}

func (s *Service) CreateQualification(actor string, in CreateQualificationInput) (domain.QualificationRecord, error) {
	id := newID("qual")
	in.Variables.Normalize()
	if err := in.Variables.Validate(); err != nil {
		return domain.QualificationRecord{}, err
	}
	now := s.clock.Now()
	q := domain.QualificationRecord{ID: id, RuleSetID: in.RuleSetID, Status: domain.QualificationUnderTest, Version: 1, Variables: in.Variables, Evidence: []domain.TestEvidence{}, CreatedAt: now, UpdatedAt: now}
	err := s.repo.Update(actor, "QUALIFICATION_CREATED", "qualification", id, func(st *domain.State) error {
		r, ok := st.Rules[in.RuleSetID]
		if !ok {
			return domain.NotFound("规则", in.RuleSetID)
		}
		if r.Status == domain.RuleDraft {
			return domain.Conflict("RULE_NOT_PUBLISHED", "评定必须绑定已发布规则")
		}
		st.Qualifications[id] = q
		return nil
	})
	return q, err
}

type QualificationFilter struct {
	Status       domain.QualificationStatus
	RuleSetID    string
	BaseMaterial string
}

func (s *Service) ListQualifications(f QualificationFilter) ([]domain.QualificationRecord, error) {
	var out []domain.QualificationRecord
	err := s.repo.View(func(st domain.State) error {
		for _, q := range st.Qualifications {
			if f.Status != "" && q.Status != f.Status {
				continue
			}
			if f.RuleSetID != "" && q.RuleSetID != f.RuleSetID {
				continue
			}
			if f.BaseMaterial != "" && !containsFold(q.Variables.BaseMaterials, f.BaseMaterial) {
				continue
			}
			out = append(out, q)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return nil
	})
	return out, err
}
func (s *Service) GetQualification(id string) (domain.QualificationRecord, error) {
	var out domain.QualificationRecord
	err := s.repo.View(func(st domain.State) error {
		v, ok := st.Qualifications[id]
		if !ok {
			return domain.NotFound("评定", id)
		}
		out = v
		return nil
	})
	return out, err
}

type AddEvidenceInput struct {
	ExpectedVersion int          `json:"expected_version"`
	Type            string       `json:"type"`
	Result          domain.Fixed `json:"result"`
	Unit            string       `json:"unit"`
	Passed          bool         `json:"passed"`
	Source          string       `json:"source"`
	TestedAt        time.Time    `json:"tested_at"`
	ExpiresAt       *time.Time   `json:"expires_at,omitempty"`
}

func (s *Service) AddEvidence(actor, id, idemKey string, in AddEvidenceInput) (domain.TestEvidence, error) {
	// 在计算幂等摘要前完成文本和试验单位归一化，保证等价请求得到同一结果。
	in.Type = strings.ToUpper(strings.TrimSpace(in.Type))
	in.Unit = strings.TrimSpace(in.Unit)
	in.Source = strings.TrimSpace(in.Source)
	if !in.TestedAt.IsZero() {
		in.TestedAt = in.TestedAt.UTC()
	}
	if in.ExpiresAt != nil {
		expiresAt := in.ExpiresAt.UTC()
		in.ExpiresAt = &expiresAt
	}
	normalizedResult, normalizedUnit, normalizeErr := domain.NormalizeMeasurement(in.Type, in.Result, in.Unit)
	if normalizeErr != nil {
		return domain.TestEvidence{}, normalizeErr
	}
	in.Result, in.Unit = normalizedResult, normalizedUnit
	requestDigest := domain.Digest(in)
	var out domain.TestEvidence
	err := s.repo.Update(actor, "EVIDENCE_ADDED", "qualification", id, func(st *domain.State) error {
		if idemKey != "" {
			if rec, ok := st.Idempotency[id+":"+idemKey]; ok {
				if rec.RequestDigest != requestDigest {
					return domain.Conflict("IDEMPOTENCY_CONFLICT", "相同幂等键对应不同请求")
				}
				return json.Unmarshal(rec.Response, &out)
			}
		}
		q, ok := st.Qualifications[id]
		if !ok {
			return domain.NotFound("评定", id)
		}
		if q.Status != domain.QualificationUnderTest {
			return domain.Conflict("QUALIFICATION_STATE_CONFLICT", "当前状态不能添加证据")
		}
		if q.Version != in.ExpectedVersion {
			return versionConflict(q.Version)
		}
		if in.Type == "" || in.Unit == "" || in.Source == "" {
			return domain.Invalid("试验类型、单位和来源不能为空", nil)
		}
		if in.TestedAt.IsZero() {
			in.TestedAt = s.clock.Now()
		}
		if in.ExpiresAt != nil && !in.ExpiresAt.After(in.TestedAt) {
			return domain.Invalid("证据有效期必须晚于试验时间", nil)
		}
		out = domain.TestEvidence{ID: newID("ev"), Type: in.Type, Result: in.Result, Unit: in.Unit, Passed: in.Passed, Source: in.Source, TestedAt: in.TestedAt.UTC(), ExpiresAt: in.ExpiresAt}
		q.Evidence = append(q.Evidence, out)
		q.Version++
		q.UpdatedAt = s.clock.Now()
		st.Qualifications[id] = q
		if idemKey != "" {
			raw, _ := json.Marshal(out)
			st.Idempotency[id+":"+idemKey] = domain.IdempotencyRecord{Key: idemKey, RequestDigest: requestDigest, Status: 201, Response: raw, CreatedAt: s.clock.Now()}
		}
		return nil
	})
	return out, err
}
func (s *Service) EvaluateQualification(actor, id string, expected int) (domain.QualificationRecord, error) {
	var out domain.QualificationRecord
	err := s.repo.Update(actor, "QUALIFICATION_EVALUATED", "qualification", id, func(st *domain.State) error {
		q, ok := st.Qualifications[id]
		if !ok {
			return domain.NotFound("评定", id)
		}
		if q.Status != domain.QualificationUnderTest {
			return domain.Conflict("QUALIFICATION_STATE_CONFLICT", "只有试验中评定可判定")
		}
		if q.Version != expected {
			return versionConflict(q.Version)
		}
		r, ok := st.Rules[q.RuleSetID]
		if !ok {
			return domain.NotFound("规则", q.RuleSetID)
		}
		reasons, passed := rules.EvaluateQualification(r, q, s.clock.Now())
		q.FailureReasons = reasons
		if passed {
			q.Status = domain.QualificationQualified
		} else {
			q.Status = domain.QualificationFailed
		}
		q.Version++
		q.UpdatedAt = s.clock.Now()
		st.Qualifications[id] = q
		out = q
		return nil
	})
	return out, err
}
func (s *Service) WithdrawQualification(actor, id string, expected int, reason string) (domain.QualificationRecord, error) {
	var out domain.QualificationRecord
	now := s.clock.Now()
	err := s.repo.Update(actor, "QUALIFICATION_WITHDRAWN", "qualification", id, func(st *domain.State) error {
		q, ok := st.Qualifications[id]
		if !ok {
			return domain.NotFound("评定", id)
		}
		if q.Status != domain.QualificationQualified {
			return domain.Conflict("QUALIFICATION_STATE_CONFLICT", "只有合格评定可以撤回")
		}
		if q.Version != expected {
			return versionConflict(q.Version)
		}
		if strings.TrimSpace(reason) == "" {
			return domain.Invalid("撤回原因不能为空", nil)
		}
		q.Status = domain.QualificationWithdrawn
		q.Version++
		q.UpdatedAt = now
		q.FailureReasons = []string{strings.TrimSpace(reason)}
		st.Qualifications[id] = q
		for rid, rev := range st.Revisions {
			if contains(rev.QualificationIDs, id) {
				enqueueAffected(st, "QUALIFICATION_WITHDRAWN", "revision", rid, id, now)
				markAssessmentsStale(st, rid, "来源评定已撤回")
			}
		}
		out = q
		return nil
	})
	return out, err
}

type CreateProcedureInput struct {
	Name             string                    `json:"name"`
	RuleSetID        string                    `json:"rule_set_id"`
	QualificationIDs []string                  `json:"qualification_ids"`
	Variables        domain.ProcedureVariables `json:"variables"`
}
type ProcedureResult struct {
	Procedure domain.ProcedureSpecification `json:"procedure"`
	Revision  domain.ProcedureRevision      `json:"revision"`
}

func (s *Service) CreateProcedure(actor string, in CreateProcedureInput) (ProcedureResult, error) {
	if strings.TrimSpace(in.Name) == "" {
		return ProcedureResult{}, domain.Invalid("规程名称不能为空", nil)
	}
	if len(in.QualificationIDs) == 0 {
		return ProcedureResult{}, domain.Invalid("至少绑定一条评定", nil)
	}
	in.Variables.Normalize()
	if err := in.Variables.Validate(); err != nil {
		return ProcedureResult{}, err
	}
	pid, rid := newID("proc"), newID("rev")
	now := s.clock.Now()
	p := domain.ProcedureSpecification{ID: pid, Name: strings.TrimSpace(in.Name), Revisions: []string{rid}, Version: 1}
	rev := domain.ProcedureRevision{ID: rid, ProcedureID: pid, Number: 1, Status: domain.RevisionDraft, Version: 1, RuleSetID: in.RuleSetID, QualificationIDs: uniqueSorted(in.QualificationIDs), Variables: in.Variables, CreatedAt: now}
	result := ProcedureResult{p, rev}
	err := s.repo.Update(actor, "PROCEDURE_CREATED", "procedure", pid, func(st *domain.State) error {
		rule, ok := st.Rules[in.RuleSetID]
		if !ok {
			return domain.NotFound("规则", in.RuleSetID)
		}
		if rule.Status != domain.RulePublished {
			return domain.Conflict("RULE_NOT_PUBLISHED", "规程必须绑定已发布规则")
		}
		for _, qid := range rev.QualificationIDs {
			q, ok := st.Qualifications[qid]
			if !ok {
				return domain.NotFound("评定", qid)
			}
			if q.Status != domain.QualificationQualified {
				return domain.Conflict("QUALIFICATION_NOT_VALID", "规程只能引用合格评定")
			}
			if q.RuleSetID != in.RuleSetID {
				return domain.Conflict("RULE_SET_MISMATCH", "来源评定必须引用同一已发布规则")
			}
		}
		st.Procedures[pid] = p
		st.Revisions[rid] = rev
		return nil
	})
	return result, err
}

type CreateRevisionInput struct {
	ExpectedProcedureVersion int                       `json:"expected_procedure_version"`
	RuleSetID                string                    `json:"rule_set_id"`
	QualificationIDs         []string                  `json:"qualification_ids"`
	Variables                domain.ProcedureVariables `json:"variables"`
}

func (s *Service) CreateRevision(actor, pid string, in CreateRevisionInput) (domain.ProcedureRevision, error) {
	in.Variables.Normalize()
	if err := in.Variables.Validate(); err != nil {
		return domain.ProcedureRevision{}, err
	}
	id := newID("rev")
	var out domain.ProcedureRevision
	err := s.repo.Update(actor, "PROCEDURE_REVISION_CREATED", "procedure", pid, func(st *domain.State) error {
		p, ok := st.Procedures[pid]
		if !ok {
			return domain.NotFound("规程", pid)
		}
		if p.Version != in.ExpectedProcedureVersion {
			return versionConflict(p.Version)
		}
		if p.PublishedRevisionID == "" {
			return domain.Conflict("NO_PUBLISHED_BASE", "创建新修订前必须先发布首版")
		}
		if len(in.QualificationIDs) == 0 {
			return domain.Invalid("至少绑定一条评定", nil)
		}
		rule, ok := st.Rules[in.RuleSetID]
		if !ok {
			return domain.NotFound("规则", in.RuleSetID)
		}
		if rule.Status != domain.RulePublished {
			return domain.Conflict("RULE_NOT_PUBLISHED", "规程修订必须绑定已发布规则")
		}
		for _, qid := range in.QualificationIDs {
			q, ok := st.Qualifications[qid]
			if !ok || q.Status != domain.QualificationQualified {
				return domain.Conflict("QUALIFICATION_NOT_VALID", "新修订引用了无效评定")
			}
			if q.RuleSetID != in.RuleSetID {
				return domain.Conflict("RULE_SET_MISMATCH", "来源评定必须引用同一已发布规则")
			}
		}
		out = domain.ProcedureRevision{ID: id, ProcedureID: pid, Number: len(p.Revisions) + 1, Status: domain.RevisionDraft, Version: 1, RuleSetID: in.RuleSetID, QualificationIDs: uniqueSorted(in.QualificationIDs), Variables: in.Variables, CreatedAt: s.clock.Now()}
		p.Revisions = append(p.Revisions, id)
		p.Version++
		st.Procedures[pid] = p
		st.Revisions[id] = out
		return nil
	})
	return out, err
}
func (s *Service) DeriveCoverage(actor, rid string, expected int) (domain.ProcedureRevision, error) {
	var out domain.ProcedureRevision
	err := s.repo.Update(actor, "COVERAGE_DERIVED", "revision", rid, func(st *domain.State) error {
		rev, ok := st.Revisions[rid]
		if !ok {
			return domain.NotFound("规程修订", rid)
		}
		if rev.Status != domain.RevisionDraft {
			return domain.Conflict("REVISION_STATE_CONFLICT", "只有草稿修订可推导范围")
		}
		if rev.Version != expected {
			return versionConflict(rev.Version)
		}
		rule, ok := st.Rules[rev.RuleSetID]
		if !ok {
			return domain.NotFound("规则", rev.RuleSetID)
		}
		for _, qid := range rev.QualificationIDs {
			q, ok := st.Qualifications[qid]
			if !ok || q.Status != domain.QualificationQualified {
				return domain.Conflict("QUALIFICATION_NOT_VALID", "来源评定不再有效")
			}
		}
		var sourceSets [][]domain.CoverageRange
		var allGaps []string
		for _, qualificationID := range rev.QualificationIDs {
			qualification := st.Qualifications[qualificationID]
			derived, gaps := rules.DeriveCoverage(rule, qualification.Variables)
			sourceSets = append(sourceSets, derived)
			for _, gap := range gaps {
				allGaps = append(allGaps, qualificationID+": "+gap)
			}
		}
		rev.Coverage, rev.CoverageGaps = rules.IntersectCoverage(sourceSets...)
		rev.CoverageGaps = append(rev.CoverageGaps, allGaps...)
		_, targetDifferences, _ := rules.Assess(rev.Coverage, rev.Variables, rule)
		for _, difference := range targetDifferences {
			rev.CoverageGaps = append(rev.CoverageGaps, "目标规程变量未被来源评定覆盖: "+difference.Variable)
		}
		sort.Strings(rev.CoverageGaps)
		rev.Status = domain.RevisionReview
		rev.Version++
		st.Revisions[rid] = rev
		out = rev
		return nil
	})
	return out, err
}
func (s *Service) GetCoverage(rid string) ([]domain.CoverageRange, []string, error) {
	var c []domain.CoverageRange
	var gaps []string
	err := s.repo.View(func(st domain.State) error {
		r, ok := st.Revisions[rid]
		if !ok {
			return domain.NotFound("规程修订", rid)
		}
		c = r.Coverage
		gaps = r.CoverageGaps
		return nil
	})
	return c, gaps, err
}
func (s *Service) GetRevision(rid string) (domain.ProcedureRevision, error) {
	var out domain.ProcedureRevision
	err := s.repo.View(func(st domain.State) error {
		v, ok := st.Revisions[rid]
		if !ok {
			return domain.NotFound("规程修订", rid)
		}
		out = v
		return nil
	})
	return out, err
}
func (s *Service) PublishRevision(actor, rid string, expected int) (domain.ProcedureRevision, error) {
	var out domain.ProcedureRevision
	now := s.clock.Now()
	err := s.repo.Update(actor, "PROCEDURE_REVISION_PUBLISHED", "revision", rid, func(st *domain.State) error {
		rev, ok := st.Revisions[rid]
		if !ok {
			return domain.NotFound("规程修订", rid)
		}
		if rev.Status != domain.RevisionReview {
			return domain.Conflict("REVISION_STATE_CONFLICT", "修订必须先完成范围推导")
		}
		if rev.Version != expected {
			return versionConflict(rev.Version)
		}
		if len(rev.CoverageGaps) > 0 {
			return domain.Conflict("COVERAGE_GAPS", "存在覆盖缺口，不能发布")
		}
		for _, qid := range rev.QualificationIDs {
			if st.Qualifications[qid].Status != domain.QualificationQualified {
				return domain.Conflict("QUALIFICATION_NOT_VALID", "来源评定不再有效")
			}
		}
		for _, review := range st.ChangeReviews {
			if review.ToRevisionID == rid && len(review.Blocking) > 0 && review.Status != domain.ReviewApproved {
				return domain.Conflict("CHANGE_REVIEW_BLOCKING", "变更审查存在阻断项")
			}
		}
		p := st.Procedures[rev.ProcedureID]
		if p.PublishedRevisionID != "" {
			old := st.Revisions[p.PublishedRevisionID]
			old.Status = domain.RevisionRetired
			old.Version++
			st.Revisions[old.ID] = old
		}
		rev.Status = domain.RevisionPublished
		rev.Version++
		rev.PublishedAt = &now
		p.PublishedRevisionID = rid
		p.Version++
		st.Revisions[rid] = rev
		st.Procedures[p.ID] = p
		out = rev
		return nil
	})
	return out, err
}

type CreateRequirementInput struct {
	Reference string                    `json:"reference"`
	Variables domain.ProcedureVariables `json:"variables"`
}

func (s *Service) CreateRequirement(actor, idemKey string, in CreateRequirementInput) (domain.JointRequirement, error) {
	in.Reference = strings.TrimSpace(in.Reference)
	in.Variables.Normalize()
	if in.Reference == "" {
		return domain.JointRequirement{}, domain.Invalid("生产接头引用号不能为空", nil)
	}
	if err := in.Variables.Validate(); err != nil {
		return domain.JointRequirement{}, err
	}
	requestDigest := domain.Digest(in)
	id := newID("joint")
	now := s.clock.Now()
	out := domain.JointRequirement{ID: id, Version: 1, Reference: in.Reference, Variables: in.Variables, CreatedAt: now}
	err := s.repo.Update(actor, "JOINT_REQUIREMENT_CREATED", "joint_requirement", id, func(st *domain.State) error {
		key := "requirement:" + idemKey
		if idemKey != "" {
			if rec, ok := st.Idempotency[key]; ok {
				if rec.RequestDigest != requestDigest {
					return domain.Conflict("IDEMPOTENCY_CONFLICT", "相同幂等键对应不同请求")
				}
				return json.Unmarshal(rec.Response, &out)
			}
		}
		st.Requirements[id] = out
		if idemKey != "" {
			raw, _ := json.Marshal(out)
			st.Idempotency[key] = domain.IdempotencyRecord{Key: idemKey, RequestDigest: requestDigest, Status: 201, Response: raw, CreatedAt: now}
		}
		return nil
	})
	return out, err
}
func (s *Service) GetRequirement(id string) (domain.JointRequirement, error) {
	var out domain.JointRequirement
	err := s.repo.View(func(st domain.State) error {
		v, ok := st.Requirements[id]
		if !ok {
			return domain.NotFound("生产接头要求", id)
		}
		out = v
		return nil
	})
	return out, err
}

type CreateAssessmentInput struct {
	RevisionID                 string `json:"revision_id"`
	ExpectedRequirementVersion int    `json:"expected_requirement_version"`
}

func (s *Service) AssessRequirement(actor, id string, in CreateAssessmentInput) (domain.ApplicabilityAssessment, error) {
	aid := newID("assess")
	var out domain.ApplicabilityAssessment
	err := s.repo.Update(actor, "APPLICABILITY_ASSESSED", "joint_requirement", id, func(st *domain.State) error {
		req, ok := st.Requirements[id]
		if !ok {
			return domain.NotFound("生产接头要求", id)
		}
		if req.Version != in.ExpectedRequirementVersion {
			return versionConflict(req.Version)
		}
		rev, ok := st.Revisions[in.RevisionID]
		if !ok {
			return domain.NotFound("规程修订", in.RevisionID)
		}
		if rev.Status != domain.RevisionPublished {
			return domain.Conflict("REVISION_NOT_PUBLISHED", "只能使用已发布规程进行评估")
		}
		rule := st.Rules[rev.RuleSetID]
		conclusion, diffs, conditions := rules.Assess(rev.Coverage, req.Variables, rule)
		out = domain.ApplicabilityAssessment{ID: aid, RequirementID: id, RevisionID: rev.ID, RuleSetID: rule.ID, Conclusion: conclusion, Differences: diffs, Conditions: conditions, CreatedAt: s.clock.Now()}
		req.AssessmentIDs = append(req.AssessmentIDs, aid)
		req.Version++
		st.Requirements[id] = req
		st.Assessments[aid] = out
		return nil
	})
	return out, err
}
func (s *Service) GetAssessment(id string) (domain.ApplicabilityAssessment, error) {
	var out domain.ApplicabilityAssessment
	err := s.repo.View(func(st domain.State) error {
		v, ok := st.Assessments[id]
		if !ok {
			return domain.NotFound("适用性评估", id)
		}
		out = v
		return nil
	})
	return out, err
}

type CreateChangeReviewInput struct {
	FromRevisionID string `json:"from_revision_id"`
	ToRevisionID   string `json:"to_revision_id"`
	RuleSetID      string `json:"rule_set_id"`
}

func (s *Service) CreateChangeReview(actor string, in CreateChangeReviewInput) (domain.ChangeReview, error) {
	id := newID("review")
	var out domain.ChangeReview
	err := s.repo.Update(actor, "CHANGE_REVIEW_CREATED", "change_review", id, func(st *domain.State) error {
		from, ok := st.Revisions[in.FromRevisionID]
		if !ok {
			return domain.NotFound("源规程修订", in.FromRevisionID)
		}
		to, ok := st.Revisions[in.ToRevisionID]
		if !ok {
			return domain.NotFound("目标规程修订", in.ToRevisionID)
		}
		if from.ProcedureID != to.ProcedureID {
			return domain.Invalid("只能比较同一规程的修订版", nil)
		}
		rule, ok := st.Rules[in.RuleSetID]
		if !ok {
			return domain.NotFound("规则", in.RuleSetID)
		}
		diffs, blocking := rules.Compare(rule, from.Variables, to.Variables)
		out = domain.ChangeReview{ID: id, FromRevisionID: from.ID, ToRevisionID: to.ID, RuleSetID: rule.ID, Status: domain.ReviewReady, Version: 1, Differences: diffs, Blocking: blocking}
		st.ChangeReviews[id] = out
		return nil
	})
	return out, err
}

type DecideReviewInput struct {
	ExpectedVersion int    `json:"expected_version"`
	Decision        string `json:"decision"`
}

func (s *Service) DecideChangeReview(actor, id string, in DecideReviewInput) (domain.ChangeReview, error) {
	var out domain.ChangeReview
	err := s.repo.Update(actor, "CHANGE_REVIEW_DECIDED", "change_review", id, func(st *domain.State) error {
		r, ok := st.ChangeReviews[id]
		if !ok {
			return domain.NotFound("变更审查", id)
		}
		if r.Status != domain.ReviewReady {
			return domain.Conflict("REVIEW_STATE_CONFLICT", "审查当前不可签发决定")
		}
		if r.Version != in.ExpectedVersion {
			return versionConflict(r.Version)
		}
		decision := strings.ToUpper(strings.TrimSpace(in.Decision))
		if len(r.Blocking) > 0 && decision == "APPROVE" {
			return domain.Conflict("REQUALIFICATION_REQUIRED", "存在重要变量变更，不能直接批准")
		}
		now := s.clock.Now()
		if decision == "APPROVE" {
			r.Status = domain.ReviewApproved
			r.Decision = "APPROVED"
		} else if decision == "REQUALIFY" {
			r.Status = domain.ReviewRequalification
			r.Decision = "REQUALIFICATION_REQUIRED"
		} else {
			return domain.Invalid("决定必须是 APPROVE 或 REQUALIFY", nil)
		}
		r.Version++
		r.DecidedAt = &now
		st.ChangeReviews[id] = r
		out = r
		return nil
	})
	return out, err
}

type TaskFilter struct {
	Status domain.TaskStatus
	Reason string
}

func (s *Service) ListTasks(f TaskFilter) ([]domain.ReviewTask, error) {
	var out []domain.ReviewTask
	err := s.repo.View(func(st domain.State) error {
		for _, t := range st.Tasks {
			if f.Status != "" && t.Status != f.Status {
				continue
			}
			if f.Reason != "" && t.Reason != f.Reason {
				continue
			}
			out = append(out, t)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return nil
	})
	return out, err
}
func (s *Service) RunDueTasks(actor string, limit int) (int, error) {
	if limit <= 0 {
		limit = 20
	}
	processed := 0
	now := s.clock.Now()
	preflight, err := s.repo.Snapshot()
	if err != nil {
		return 0, err
	}
	due := false
	for _, task := range preflight.Tasks {
		if task.Status == domain.TaskPending && !task.NextRunAt.After(now) {
			due = true
			break
		}
	}
	if !due {
		return 0, nil
	}
	err = s.repo.Update(actor, "REVIEW_TASKS_PROCESSED", "review_task", "batch", func(st *domain.State) error {
		ids := make([]string, 0)
		for id, t := range st.Tasks {
			if t.Status == domain.TaskPending && !t.NextRunAt.After(now) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if len(ids) > limit {
			ids = ids[:limit]
		}
		for _, id := range ids {
			t := st.Tasks[id]
			t.Status = domain.TaskRunning
			t.Attempts++
			t.UpdatedAt = now
			switch t.ObjectType {
			case "assessment":
				a, ok := st.Assessments[t.ObjectID]
				if ok {
					a.Stale = true
					a.StaleReason = t.Reason
					st.Assessments[a.ID] = a
				}
			case "revision":
				markAssessmentsStale(st, t.ObjectID, t.Reason)
			}
			t.Status = domain.TaskDone
			t.UpdatedAt = now
			st.Tasks[id] = t
			processed++
		}
		return nil
	})
	return processed, err
}
func (s *Service) ScanExpiredEvidence(actor string) (int, error) {
	now := s.clock.Now()
	created := 0
	preflight, err := s.repo.Snapshot()
	if err != nil {
		return 0, err
	}
	found := false
	for qualificationID, qualification := range preflight.Qualifications {
		for _, evidence := range qualification.Evidence {
			if evidence.ExpiresAt == nil || !now.After(*evidence.ExpiresAt) || evidence.RevokedAt != nil {
				continue
			}
			key := "EVIDENCE_EXPIRED:qualification:" + qualificationID + ":" + evidence.ID
			if !taskActiveByKey(&preflight, key) {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return 0, nil
	}
	err = s.repo.Update(actor, "EXPIRED_EVIDENCE_SCANNED", "evidence", "batch", func(st *domain.State) error {
		for qid, q := range st.Qualifications {
			for _, e := range q.Evidence {
				if e.ExpiresAt == nil || !now.After(*e.ExpiresAt) || e.RevokedAt != nil {
					continue
				}
				key := "EVIDENCE_EXPIRED:qualification:" + qid + ":" + e.ID
				if taskActiveByKey(st, key) {
					continue
				}
				enqueueAffected(st, "EVIDENCE_EXPIRED", "qualification", qid, e.ID, now)
				for rid, rev := range st.Revisions {
					if contains(rev.QualificationIDs, qid) {
						enqueueAffected(st, "EVIDENCE_EXPIRED", "revision", rid, e.ID, now)
						markAssessmentsStale(st, rid, "来源证据已过期")
					}
				}
				created++
			}
		}
		return nil
	})
	return created, err
}

type Health struct {
	Status         string    `json:"status"`
	Sequence       uint64    `json:"sequence"`
	AuditValid     bool      `json:"audit_valid"`
	DegradedReason string    `json:"degraded_reason,omitempty"`
	PendingTasks   int       `json:"pending_tasks"`
	DeadTasks      int       `json:"dead_tasks"`
	Time           time.Time `json:"time"`
}

func (s *Service) Health() Health {
	h := Health{Status: "ok", AuditValid: true, Time: s.clock.Now()}
	if err := s.repo.Probe(); err != nil {
		h.Status = "unhealthy"
		h.AuditValid = false
		h.DegradedReason = err.Error()
		return h
	}
	st, err := s.repo.Snapshot()
	if err != nil {
		h.Status = "unhealthy"
		h.AuditValid = false
		h.DegradedReason = err.Error()
		return h
	}
	h.Sequence = st.Sequence
	if err = store.ValidateState(st); err != nil {
		h.Status = "unhealthy"
		h.AuditValid = false
		h.DegradedReason = err.Error()
	}
	for _, t := range st.Tasks {
		if t.Status == domain.TaskPending {
			h.PendingTasks++
		}
		if t.Status == domain.TaskDead {
			h.DeadTasks++
		}
	}
	degraded, reason := s.repo.Degraded()
	if degraded && h.Status == "ok" {
		h.Status = "degraded"
		h.DegradedReason = reason
	}
	if h.DeadTasks > 0 {
		h.Status = "degraded"
		h.DegradedReason = "存在耗尽重试的复核任务"
	}
	return h
}

type CaseExport struct {
	SchemaVersion int                 `json:"schema_version"`
	ExportedAt    time.Time           `json:"exported_at"`
	ObjectType    string              `json:"object_type"`
	ObjectID      string              `json:"object_id"`
	Object        any                 `json:"object"`
	Related       map[string]any      `json:"related"`
	Audit         []domain.AuditEvent `json:"audit"`
	Digest        string              `json:"digest"`
}

func (s *Service) ExportCase(objectType, id string) (CaseExport, error) {
	var out CaseExport
	err := s.repo.View(func(st domain.State) error {
		out = CaseExport{SchemaVersion: 1, ExportedAt: s.clock.Now(), ObjectType: objectType, ObjectID: id, Related: map[string]any{}}
		switch objectType {
		case "assessment":
			a, ok := st.Assessments[id]
			if !ok {
				return domain.NotFound("适用性评估", id)
			}
			out.Object = a
			out.Related["requirement"] = st.Requirements[a.RequirementID]
			out.Related["revision"] = st.Revisions[a.RevisionID]
			out.Related["rule_set"] = st.Rules[a.RuleSetID]
		case "change-review":
			r, ok := st.ChangeReviews[id]
			if !ok {
				return domain.NotFound("变更审查", id)
			}
			out.Object = r
			out.Related["from_revision"] = st.Revisions[r.FromRevisionID]
			out.Related["to_revision"] = st.Revisions[r.ToRevisionID]
			out.Related["rule_set"] = st.Rules[r.RuleSetID]
		case "qualification":
			q, ok := st.Qualifications[id]
			if !ok {
				return domain.NotFound("评定", id)
			}
			out.Object = q
			out.Related["rule_set"] = st.Rules[q.RuleSetID]
		default:
			return domain.Invalid("不支持的审查包对象类型", []string{"assessment", "change-review", "qualification"})
		}
		for _, e := range st.Audit {
			if e.ObjectID == id {
				out.Audit = append(out.Audit, e)
			}
		}
		unsigned := out
		unsigned.Digest = ""
		out.Digest = domain.Digest(unsigned)
		return nil
	})
	return out, err
}

func (s *Service) ImportRequirements(actor, batchKey string, inputs []CreateRequirementInput, dryRun bool) ([]domain.JointRequirement, error) {
	if len(inputs) == 0 {
		return nil, domain.Invalid("导入文件没有记录", nil)
	}
	if len(inputs) > 1000 {
		return nil, domain.Invalid("单批最多导入 1000 条", nil)
	}
	normalized := make([]CreateRequirementInput, len(inputs))
	for i, in := range inputs {
		in.Reference = strings.TrimSpace(in.Reference)
		in.Variables.Normalize()
		if in.Reference == "" {
			return nil, domain.Invalid(fmt.Sprintf("第 %d 条引用号为空", i+1), nil)
		}
		if err := in.Variables.Validate(); err != nil {
			return nil, domain.Invalid(fmt.Sprintf("第 %d 条无效: %v", i+1, err), nil)
		}
		normalized[i] = in
	}
	if dryRun {
		return buildRequirements(normalized, s.clock.Now()), nil
	}
	digest := domain.Digest(normalized)
	var out []domain.JointRequirement
	err := s.repo.Update(actor, "JOINT_REQUIREMENTS_IMPORTED", "joint_requirement", "batch", func(st *domain.State) error {
		key := "batch:" + batchKey
		if batchKey != "" {
			if rec, ok := st.Idempotency[key]; ok {
				if rec.RequestDigest != digest {
					return domain.Conflict("IDEMPOTENCY_CONFLICT", "批次键已用于不同内容")
				}
				return json.Unmarshal(rec.Response, &out)
			}
		}
		out = buildRequirements(normalized, s.clock.Now())
		for _, r := range out {
			st.Requirements[r.ID] = r
		}
		if batchKey != "" {
			raw, _ := json.Marshal(out)
			st.Idempotency[key] = domain.IdempotencyRecord{Key: batchKey, RequestDigest: digest, Status: 201, Response: raw, CreatedAt: s.clock.Now()}
		}
		return nil
	})
	return out, err
}

func buildRequirements(inputs []CreateRequirementInput, now time.Time) []domain.JointRequirement {
	out := make([]domain.JointRequirement, len(inputs))
	for i, in := range inputs {
		out[i] = domain.JointRequirement{ID: newID("joint"), Version: 1, Reference: in.Reference, Variables: in.Variables, CreatedAt: now}
	}
	return out
}
func enqueueAffected(st *domain.State, reason, objectType, objectID, trigger string, now time.Time) {
	key := reason + ":" + objectType + ":" + objectID + ":" + trigger
	if taskActiveByKey(st, key) {
		return
	}
	id := newID("task")
	st.Tasks[id] = domain.ReviewTask{ID: id, Key: key, Reason: reason, ObjectType: objectType, ObjectID: objectID, TriggerVersion: trigger, Status: domain.TaskPending, MaxAttempts: 3, NextRunAt: now, CreatedAt: now, UpdatedAt: now}
}
func taskActiveByKey(st *domain.State, key string) bool {
	for _, t := range st.Tasks {
		if t.Key == key && (t.Status == domain.TaskPending || t.Status == domain.TaskRunning || t.Status == domain.TaskDone) {
			return true
		}
	}
	return false
}
func markAssessmentsStale(st *domain.State, revisionID, reason string) {
	for id, a := range st.Assessments {
		if a.RevisionID == revisionID && !a.Stale {
			a.Stale = true
			a.StaleReason = reason
			st.Assessments[id] = a
		}
	}
}
func versionConflict(current int) error {
	return &domain.Error{Code: "VERSION_CONFLICT", Message: "聚合版本冲突", Details: map[string]int{"current_version": current}, Status: 409}
}
func contains(v []string, target string) bool {
	for _, x := range v {
		if x == target {
			return true
		}
	}
	return false
}
func containsFold(v []string, target string) bool {
	for _, x := range v {
		if strings.EqualFold(x, target) {
			return true
		}
	}
	return false
}
func uniqueSorted(in []string) []string {
	set := map[string]bool{}
	for _, x := range in {
		if x = strings.TrimSpace(x); x != "" {
			set[x] = true
		}
	}
	out := make([]string, 0, len(set))
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}
func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
