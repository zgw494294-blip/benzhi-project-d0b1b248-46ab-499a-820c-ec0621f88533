package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"arcproof/internal/app"
	"arcproof/internal/domain"
)

const maxBodyBytes = 1 << 20

type Server struct {
	service  *app.Service
	logger   *slog.Logger
	handler  http.Handler
	requests atomic.Uint64
}
type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Details   any    `json:"details,omitempty"`
	RequestID string `json:"request_id"`
}
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func New(service *app.Service, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{service: service, logger: logger}
	mux := http.NewServeMux()
	s.routes(mux)
	mux.Handle("/", http.HandlerFunc(s.notFound))
	s.handler = s.middleware(mux)
	return s
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.writeStatusError(w, r, http.StatusNotFound, "ROUTE_NOT_FOUND", "请求的 API 路由不存在", nil)
}
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/healthz", s.health)
	mux.HandleFunc("POST /api/v1/rule-sets", s.createRule)
	mux.HandleFunc("PUT /api/v1/rule-sets/{id}", s.updateRule)
	mux.HandleFunc("POST /api/v1/rule-sets/{id}/publish", s.publishRule)
	mux.HandleFunc("GET /api/v1/rule-sets/{id}", s.getRule)
	mux.HandleFunc("POST /api/v1/qualifications", s.createQualification)
	mux.HandleFunc("GET /api/v1/qualifications", s.listQualifications)
	mux.HandleFunc("GET /api/v1/qualifications/{id}", s.getQualification)
	mux.HandleFunc("POST /api/v1/qualifications/{id}/evidence", s.addEvidence)
	mux.HandleFunc("POST /api/v1/qualifications/{id}/evaluate", s.evaluateQualification)
	mux.HandleFunc("POST /api/v1/qualifications/{id}/withdraw", s.withdrawQualification)
	mux.HandleFunc("POST /api/v1/procedures", s.createProcedure)
	mux.HandleFunc("POST /api/v1/procedures/{id}/revisions", s.createRevision)
	mux.HandleFunc("POST /api/v1/procedure-revisions/{id}/derive-coverage", s.deriveCoverage)
	mux.HandleFunc("GET /api/v1/procedure-revisions/{id}/coverage", s.getCoverage)
	mux.HandleFunc("POST /api/v1/procedure-revisions/{id}/publish", s.publishRevision)
	mux.HandleFunc("POST /api/v1/joint-requirements", s.createRequirement)
	mux.HandleFunc("GET /api/v1/joint-requirements/{id}", s.getRequirement)
	mux.HandleFunc("POST /api/v1/joint-requirements/{id}/assessments", s.createAssessment)
	mux.HandleFunc("GET /api/v1/assessments/{id}", s.getAssessment)
	mux.HandleFunc("POST /api/v1/change-reviews", s.createChangeReview)
	mux.HandleFunc("POST /api/v1/change-reviews/{id}/decide", s.decideReview)
	mux.HandleFunc("GET /api/v1/review-tasks", s.listTasks)
	mux.HandleFunc("GET /api/v1/cases/{object_type}/{object_id}/export", s.exportCase)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("req-%016x", s.requests.Add(1))
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		rw := &responseWriter{ResponseWriter: w, status: 200}
		started := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("请求处理异常", "request_id", id, "error", recovered)
				s.writeError(rw, r, fmt.Errorf("内部异常"))
			}
			s.logger.Info("请求完成", "request_id", id, "actor", r.Header.Get("X-Actor"), "method", r.Method, "path", r.URL.Path, "status", rw.status, "duration_ms", time.Since(started).Milliseconds())
		}()
		next.ServeHTTP(rw, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	h := s.service.Health()
	status := http.StatusOK
	if h.Status == "unhealthy" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, h)
}
func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in app.CreateRuleSetInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateRuleSet(actor, in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in app.UpdateRuleSetInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.UpdateRuleSet(actor, r.PathValue("id"), in)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) publishRule(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in expectedVersionInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.PublishRuleSet(actor, r.PathValue("id"), in.ExpectedVersion)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) getRule(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.GetRuleSet(r.PathValue("id"))
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) createQualification(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in app.CreateQualificationInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateQualification(actor, in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) listQualifications(w http.ResponseWriter, r *http.Request) {
	limit, after, ok := s.page(w, r)
	if !ok {
		return
	}
	all, err := s.service.ListQualifications(app.QualificationFilter{Status: domain.QualificationStatus(r.URL.Query().Get("status")), RuleSetID: r.URL.Query().Get("rule_set_id"), BaseMaterial: r.URL.Query().Get("base_material")})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, next := paginateQualifications(all, after, limit)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) getQualification(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.GetQualification(r.PathValue("id"))
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) addEvidence(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		s.writeError(w, r, domain.Invalid("添加证据必须提供 Idempotency-Key", nil))
		return
	}
	var in app.AddEvidenceInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.AddEvidence(actor, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) evaluateQualification(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in expectedVersionInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.EvaluateQualification(actor, r.PathValue("id"), in.ExpectedVersion)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) withdrawQualification(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int    `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.WithdrawQualification(actor, r.PathValue("id"), in.ExpectedVersion, in.Reason)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) createProcedure(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in app.CreateProcedureInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateProcedure(actor, in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) createRevision(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in app.CreateRevisionInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateRevision(actor, r.PathValue("id"), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) deriveCoverage(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in expectedVersionInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.DeriveCoverage(actor, r.PathValue("id"), in.ExpectedVersion)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) getCoverage(w http.ResponseWriter, r *http.Request) {
	coverage, gaps, err := s.service.GetCoverage(r.PathValue("id"))
	s.result(w, r, http.StatusOK, map[string]any{"coverage": coverage, "gaps": gaps}, err)
}
func (s *Server) publishRevision(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in expectedVersionInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.PublishRevision(actor, r.PathValue("id"), in.ExpectedVersion)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) createRequirement(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		s.writeError(w, r, domain.Invalid("登记生产接头必须提供 Idempotency-Key", nil))
		return
	}
	var in app.CreateRequirementInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateRequirement(actor, r.Header.Get("Idempotency-Key"), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) getRequirement(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.GetRequirement(r.PathValue("id"))
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) createAssessment(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in app.CreateAssessmentInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.AssessRequirement(actor, r.PathValue("id"), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) getAssessment(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.GetAssessment(r.PathValue("id"))
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) createChangeReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in app.CreateChangeReviewInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateChangeReview(actor, in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) decideReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.actor(w, r)
	if !ok {
		return
	}
	var in app.DecideReviewInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.DecideChangeReview(actor, r.PathValue("id"), in)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	limit, after, ok := s.page(w, r)
	if !ok {
		return
	}
	all, err := s.service.ListTasks(app.TaskFilter{Status: domain.TaskStatus(r.URL.Query().Get("status")), Reason: r.URL.Query().Get("reason")})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, next := paginateTasks(all, after, limit)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) exportCase(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.ExportCase(r.PathValue("object_type"), r.PathValue("object_id"))
	s.result(w, r, http.StatusOK, out, err)
}

type expectedVersionInput struct {
	ExpectedVersion int `json:"expected_version"`
}

func (s *Server) result(w http.ResponseWriter, r *http.Request, status int, value any, err error) {
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, status, value)
}
func (s *Server) actor(w http.ResponseWriter, r *http.Request) (string, bool) {
	actor := strings.TrimSpace(r.Header.Get("X-Actor"))
	if actor == "" {
		s.writeError(w, r, domain.Invalid("写请求必须提供 X-Actor", nil))
		return "", false
	}
	if len(actor) > 100 {
		s.writeError(w, r, domain.Invalid("X-Actor 最长 100 字符", nil))
		return "", false
	}
	return actor, true
}
func (s *Server) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.writeStatusError(w, r, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "请求体超过 1 MiB", nil)
		} else {
			s.writeError(w, r, domain.Invalid("读取请求体失败", nil))
		}
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		s.writeError(w, r, domain.Invalid("请求体不能为空", nil))
		return false
	}
	if err = rejectDuplicateKeys(raw); err != nil {
		s.writeError(w, r, domain.Invalid(err.Error(), nil))
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(target); err != nil {
		s.writeError(w, r, domain.Invalid("JSON 格式或字段无效: "+err.Error(), nil))
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		s.writeError(w, r, domain.Invalid("请求体只能包含一个 JSON 值", nil))
		return false
	}
	return true
}
func rejectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSONValue(dec, "$"); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("JSON 尾部存在多余内容")
	}
	return nil
}

// DecodeStrict 供离线批导入口复用与 HTTP 相同的字段约束。
func DecodeStrict(raw []byte, target any) error {
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("只能包含一个 JSON 值")
	}
	return nil
}
func scanJSONValue(dec *json.Decoder, path string) error {
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("JSON 无效: %v", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("对象键不是字符串")
			}
			if seen[key] {
				return fmt.Errorf("JSON 字段重复: %s.%s", path, key)
			}
			seen[key] = true
			if err = scanJSONValue(dec, path+"."+key); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("JSON 对象未闭合")
		}
	case '[':
		index := 0
		for dec.More() {
			if err = scanJSONValue(dec, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("JSON 数组未闭合")
		}
	default:
		return fmt.Errorf("JSON 分隔符无效")
	}
	return nil
}
func (s *Server) page(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 200 {
			s.writeError(w, r, domain.Invalid("limit 必须在 1 到 200 之间", nil))
			return 0, "", false
		}
		limit = v
	}
	after := ""
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			s.writeError(w, r, domain.Invalid("游标无效", nil))
			return 0, "", false
		}
		after = string(b)
	}
	return limit, after, true
}
func paginateQualifications(all []domain.QualificationRecord, after string, limit int) ([]domain.QualificationRecord, string) {
	start := 0
	for i, v := range all {
		if v.ID > after {
			start = i
			break
		}
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	next := ""
	if end < len(all) && end > 0 {
		next = base64.RawURLEncoding.EncodeToString([]byte(all[end-1].ID))
	}
	return all[start:end], next
}
func paginateTasks(all []domain.ReviewTask, after string, limit int) ([]domain.ReviewTask, string) {
	start := 0
	for i, v := range all {
		if v.ID > after {
			start = i
			break
		}
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	next := ""
	if end < len(all) && end > 0 {
		next = base64.RawURLEncoding.EncodeToString([]byte(all[end-1].ID))
	}
	return all[start:end], next
}
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		status := de.Status
		if status == 0 {
			status = 500
		}
		s.writeStatusError(w, r, status, de.Code, de.Message, de.Details)
		return
	}
	s.logger.Error("内部错误", "request_id", requestID(r), "error", err)
	s.writeStatusError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "服务内部错误", nil)
}
func (s *Server) writeStatusError(w http.ResponseWriter, r *http.Request, status int, code, message string, details any) {
	writeJSON(w, status, errorResponse{Code: code, Message: message, Details: details, RequestID: requestID(r)})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type requestIDKey struct{}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}
