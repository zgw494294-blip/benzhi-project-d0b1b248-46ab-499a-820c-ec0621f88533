package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"arcproof/internal/app"
	"arcproof/internal/clock"
	"arcproof/internal/domain"
	"arcproof/internal/httpapi"
	"arcproof/internal/jobs"
	"arcproof/internal/sample"
	"arcproof/internal/store"
)

type harness struct {
	repo     *store.Repository
	service  *app.Service
	manager  *jobs.Manager
	server   *http.Server
	listener net.Listener
	client   *http.Client
	baseURL  string
	cancel   context.CancelFunc
}

func startHarness(t *testing.T, dir string, background bool) *harness {
	t.Helper()
	repo, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	service := app.New(repo, clock.Real{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: httpapi.New(service, logger).Handler(), ReadHeaderTimeout: time.Second}
	h := &harness{repo: repo, service: service, server: server, listener: listener, client: &http.Client{Timeout: 2 * time.Second}, baseURL: "http://" + listener.Addr().String()}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	if background {
		h.manager = jobs.New(service, logger).WithIntervals(10*time.Millisecond, 20*time.Millisecond)
		h.manager.Start(ctx)
	}
	go func() { _ = server.Serve(listener) }()
	waitHealth(t, h)
	return h
}
func (h *harness) close(t *testing.T) {
	t.Helper()
	h.cancel()
	if h.manager != nil {
		h.manager.Stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.server.Shutdown(ctx); err != nil {
		t.Error(err)
	}
	if err := h.repo.Close(); err != nil {
		t.Error(err)
	}
}
func waitHealth(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := h.client.Get(h.baseURL + "/api/v1/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("服务健康检查超时: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type chain struct {
	Rule          domain.RuleSet
	Qualification domain.QualificationRecord
	Procedure     domain.ProcedureSpecification
	Revision      domain.ProcedureRevision
	Requirement   domain.JointRequirement
	Assessment    domain.ApplicabilityAssessment
}

func createApplicableChain(t *testing.T, h *harness) chain {
	t.Helper()
	rule := post[domain.RuleSet](t, h, "/api/v1/rule-sets", sample.Rule(), http.StatusCreated)
	rule = post[domain.RuleSet](t, h, "/api/v1/rule-sets/"+rule.ID+"/publish", map[string]int{"expected_version": rule.Version}, http.StatusOK)
	qualification := post[domain.QualificationRecord](t, h, "/api/v1/qualifications", app.CreateQualificationInput{RuleSetID: rule.ID, Variables: sample.Variables()}, http.StatusCreated)
	for i, e := range sample.Evidence(qualification.Version) {
		request(t, h, "POST", fmt.Sprintf("/api/v1/qualifications/%s/evidence", qualification.ID), e, http.StatusCreated, fmt.Sprintf("evidence-%d", i), nil)
	}
	qualification = get[domain.QualificationRecord](t, h, "/api/v1/qualifications/"+qualification.ID)
	qualification = post[domain.QualificationRecord](t, h, "/api/v1/qualifications/"+qualification.ID+"/evaluate", map[string]int{"expected_version": qualification.Version}, http.StatusOK)
	if qualification.Status != domain.QualificationQualified {
		t.Fatalf("评定未合格: %+v", qualification.FailureReasons)
	}
	created := post[app.ProcedureResult](t, h, "/api/v1/procedures", app.CreateProcedureInput{Name: "WPS-2026-001", RuleSetID: rule.ID, QualificationIDs: []string{qualification.ID}, Variables: sample.Variables()}, http.StatusCreated)
	revision := post[domain.ProcedureRevision](t, h, "/api/v1/procedure-revisions/"+created.Revision.ID+"/derive-coverage", map[string]int{"expected_version": created.Revision.Version}, http.StatusOK)
	revision = post[domain.ProcedureRevision](t, h, "/api/v1/procedure-revisions/"+revision.ID+"/publish", map[string]int{"expected_version": revision.Version}, http.StatusOK)
	requirement := postWithKey[domain.JointRequirement](t, h, "/api/v1/joint-requirements", app.CreateRequirementInput{Reference: "JOINT-001", Variables: sample.Variables()}, http.StatusCreated, "joint-001")
	assessment := post[domain.ApplicabilityAssessment](t, h, "/api/v1/joint-requirements/"+requirement.ID+"/assessments", app.CreateAssessmentInput{RevisionID: revision.ID, ExpectedRequirementVersion: requirement.Version}, http.StatusCreated)
	if assessment.Conclusion != "APPLICABLE" {
		t.Fatalf("预期适用，实际 %s: %+v", assessment.Conclusion, assessment.Differences)
	}
	return chain{rule, qualification, created.Procedure, revision, requirement, assessment}
}

func TestQualificationToApplicability(t *testing.T) {
	dir := t.TempDir()
	h := startHarness(t, dir, false)
	result := createApplicableChain(t, h)
	exported := get[app.CaseExport](t, h, "/api/v1/cases/assessment/"+result.Assessment.ID+"/export")
	if exported.Digest == "" || exported.ObjectID != result.Assessment.ID {
		t.Fatal("审查包不完整")
	}
	h.close(t)
	h = startHarness(t, dir, false)
	defer h.close(t)
	replayed := get[domain.ApplicabilityAssessment](t, h, "/api/v1/assessments/"+result.Assessment.ID)
	if replayed.Conclusion != "APPLICABLE" || replayed.Stale {
		t.Fatalf("重启恢复结果异常: %+v", replayed)
	}
	health := get[app.Health](t, h, "/api/v1/healthz")
	if !health.AuditValid || health.Sequence < 8 {
		t.Fatalf("审计健康状态异常: %+v", health)
	}
}

func TestChangeReviewAndRuleUpgradeRecovery(t *testing.T) {
	dir := t.TempDir()
	h := startHarness(t, dir, true)
	result := createApplicableChain(t, h)
	changed := sample.Variables()
	changed.Thickness = domain.MustFixed("20")
	revision := post[domain.ProcedureRevision](t, h, "/api/v1/procedures/"+result.Procedure.ID+"/revisions", app.CreateRevisionInput{ExpectedProcedureVersion: 2, RuleSetID: result.Rule.ID, QualificationIDs: []string{result.Qualification.ID}, Variables: changed}, http.StatusCreated)
	review := post[domain.ChangeReview](t, h, "/api/v1/change-reviews", app.CreateChangeReviewInput{FromRevisionID: result.Revision.ID, ToRevisionID: revision.ID, RuleSetID: result.Rule.ID}, http.StatusCreated)
	if len(review.Blocking) == 0 {
		t.Fatal("重要变量变更应产生阻断项")
	}
	review = post[domain.ChangeReview](t, h, "/api/v1/change-reviews/"+review.ID+"/decide", app.DecideReviewInput{ExpectedVersion: review.Version, Decision: "REQUALIFY"}, http.StatusOK)
	if review.Status != domain.ReviewRequalification {
		t.Fatalf("变更决定异常: %s", review.Status)
	}
	newRuleInput := sample.Rule()
	newRuleInput.Edition = "2027-A"
	newRule := post[domain.RuleSet](t, h, "/api/v1/rule-sets", newRuleInput, http.StatusCreated)
	_ = post[domain.RuleSet](t, h, "/api/v1/rule-sets/"+newRule.ID+"/publish", map[string]int{"expected_version": newRule.Version}, http.StatusOK)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var page struct {
			Items []domain.ReviewTask `json:"items"`
		}
		request(t, h, "GET", "/api/v1/review-tasks?reason=RULE_UPGRADE", nil, http.StatusOK, "", &page)
		done := false
		for _, task := range page.Items {
			if task.Status == domain.TaskDone {
				done = true
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("后台规则升级复核未完成")
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.close(t)
	h = startHarness(t, dir, false)
	defer h.close(t)
	assessment := get[domain.ApplicabilityAssessment](t, h, "/api/v1/assessments/"+result.Assessment.ID)
	if !assessment.Stale || assessment.StaleReason == "" {
		t.Fatalf("历史结论未保留待复核标记: %+v", assessment)
	}
	persistedReview := get[app.CaseExport](t, h, "/api/v1/cases/change-review/"+review.ID+"/export")
	if persistedReview.Digest == "" {
		t.Fatal("重启后变更审查包缺少摘要")
	}
}

func post[T any](t *testing.T, h *harness, path string, body any, status int) T {
	return postWithKey[T](t, h, path, body, status, "")
}
func postWithKey[T any](t *testing.T, h *harness, path string, body any, status int, key string) T {
	t.Helper()
	var out T
	request(t, h, "POST", path, body, status, key, &out)
	return out
}
func get[T any](t *testing.T, h *harness, path string) T {
	t.Helper()
	var out T
	request(t, h, "GET", path, nil, http.StatusOK, "", &out)
	return out
}
func request(t *testing.T, h *harness, method, path string, body any, want int, key string, target any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.baseURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Actor", "acceptance-tester")
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: 状态 %d，期望 %d，响应 %s", method, path, resp.StatusCode, want, raw)
	}
	if target != nil {
		if err = json.Unmarshal(raw, target); err != nil {
			t.Fatalf("解析响应失败: %v，响应 %s", err, raw)
		}
	}
}
