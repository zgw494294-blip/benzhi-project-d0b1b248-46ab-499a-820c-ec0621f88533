package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 1

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
	Status  int    `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func Invalid(msg string, details any) *Error {
	return &Error{Code: "VALIDATION_FAILED", Message: msg, Details: details, Status: 422}
}
func Conflict(code, msg string) *Error { return &Error{Code: code, Message: msg, Status: 409} }
func NotFound(kind, id string) *Error {
	return &Error{Code: "NOT_FOUND", Message: fmt.Sprintf("未找到%s %s", kind, id), Status: 404}
}

type Fixed int64

func ParseFixed(s string) (Fixed, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("数值不能为空")
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("非法定点数")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("非法定点数")
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > 3 {
		return 0, fmt.Errorf("最多支持三位小数")
	}
	for len(frac) < 3 {
		frac += "0"
	}
	fv := int64(0)
	if frac != "" {
		fv, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("非法定点数")
		}
	}
	v := whole*1000 + fv
	if neg {
		v = -v
	}
	return Fixed(v), nil
}
func MustFixed(s string) Fixed {
	v, e := ParseFixed(s)
	if e != nil {
		panic(e)
	}
	return v
}
func (f Fixed) String() string {
	v := int64(f)
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%03d", sign, v/1000, v%1000)
}
func (f Fixed) MarshalJSON() ([]byte, error) { return json.Marshal(f.String()) }
func (f *Fixed) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("定点数必须是字符串")
	}
	v, err := ParseFixed(s)
	if err == nil {
		*f = v
	}
	return err
}

type Interval struct {
	Min  Fixed  `json:"min"`
	Max  Fixed  `json:"max"`
	Unit string `json:"unit"`
}

func (i Interval) Valid() bool           { return i.Unit != "" && i.Min <= i.Max }
func (i Interval) Contains(v Fixed) bool { return v >= i.Min && v <= i.Max }
func (i Interval) Intersect(o Interval) (Interval, bool) {
	if i.Unit != o.Unit {
		return Interval{}, false
	}
	r := Interval{Min: max(i.Min, o.Min), Max: min(i.Max, o.Max), Unit: i.Unit}
	return r, r.Valid()
}

type RuleStatus string

const (
	RuleDraft      RuleStatus = "DRAFT"
	RulePublished  RuleStatus = "PUBLISHED"
	RuleSuperseded RuleStatus = "SUPERSEDED"
)

type QualificationStatus string

const (
	QualificationDraft     QualificationStatus = "DRAFT"
	QualificationUnderTest QualificationStatus = "UNDER_TEST"
	QualificationQualified QualificationStatus = "QUALIFIED"
	QualificationFailed    QualificationStatus = "FAILED"
	QualificationWithdrawn QualificationStatus = "WITHDRAWN"
)

type RevisionStatus string

const (
	RevisionDraft     RevisionStatus = "DRAFT"
	RevisionReview    RevisionStatus = "IN_REVIEW"
	RevisionPublished RevisionStatus = "PUBLISHED"
	RevisionRetired   RevisionStatus = "RETIRED"
)

type ReviewStatus string

const (
	ReviewOpen            ReviewStatus = "OPEN"
	ReviewReady           ReviewStatus = "READY"
	ReviewApproved        ReviewStatus = "APPROVED"
	ReviewRequalification ReviewStatus = "REQUALIFICATION_REQUIRED"
	ReviewVoid            ReviewStatus = "VOID"
)

type TaskStatus string

const (
	TaskPending TaskStatus = "PENDING"
	TaskRunning TaskStatus = "RUNNING"
	TaskDone    TaskStatus = "DONE"
	TaskDead    TaskStatus = "DEAD"
)

type VariableClass string

const (
	ClassNonEssential  VariableClass = "NON_ESSENTIAL"
	ClassEssential     VariableClass = "ESSENTIAL"
	ClassSupplementary VariableClass = "SUPPLEMENTARY_ESSENTIAL"
)

type VariableRule struct {
	Name         string        `json:"name"`
	Class        VariableClass `json:"class"`
	Clause       string        `json:"clause"`
	Numeric      *Interval     `json:"numeric,omitempty"`
	Values       []string      `json:"values,omitempty"`
	RequiredWhen string        `json:"required_when,omitempty"`
}
type RequiredTest struct {
	Type         string `json:"type"`
	MinResult    Fixed  `json:"min_result"`
	Unit         string `json:"unit"`
	RequiredWhen string `json:"required_when,omitempty"`
}
type RuleSet struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Edition       string         `json:"edition"`
	Status        RuleStatus     `json:"status"`
	Version       int            `json:"version"`
	Variables     []VariableRule `json:"variables"`
	RequiredTests []RequiredTest `json:"required_tests"`
	PublishedAt   *time.Time     `json:"published_at,omitempty"`
	Digest        string         `json:"digest"`
}

type ProcedureVariables struct {
	BaseMaterials []string `json:"base_materials"`
	Process       string   `json:"process"`
	JointType     string   `json:"joint_type"`
	Position      string   `json:"position"`
	Thickness     Fixed    `json:"thickness"`
	Diameter      Fixed    `json:"diameter"`
	Filler        string   `json:"filler"`
	Preheat       Fixed    `json:"preheat"`
	HeatInput     Fixed    `json:"heat_input"`
	PWHT          string   `json:"pwht"`
	Service       string   `json:"service"`
}

func (v *ProcedureVariables) Normalize() {
	for i := range v.BaseMaterials {
		v.BaseMaterials[i] = strings.ToUpper(strings.TrimSpace(v.BaseMaterials[i]))
	}
	sort.Strings(v.BaseMaterials)
	v.Process = strings.ToUpper(strings.TrimSpace(v.Process))
	v.JointType = strings.ToUpper(strings.TrimSpace(v.JointType))
	v.Position = strings.ToUpper(strings.TrimSpace(v.Position))
	v.Filler = strings.ToUpper(strings.TrimSpace(v.Filler))
	v.PWHT = strings.ToUpper(strings.TrimSpace(v.PWHT))
	v.Service = strings.ToUpper(strings.TrimSpace(v.Service))
}
func (v ProcedureVariables) Validate() error {
	if len(v.BaseMaterials) != 2 {
		return Invalid("母材必须恰好包含两个组别", nil)
	}
	if v.Process == "" || v.JointType == "" || v.Filler == "" {
		return Invalid("焊接方法、接头形式和焊材不能为空", nil)
	}
	if v.Thickness <= 0 || v.Diameter <= 0 {
		return Invalid("厚度和直径必须大于零", nil)
	}
	return nil
}

type QualificationRecord struct {
	ID             string              `json:"id"`
	RuleSetID      string              `json:"rule_set_id"`
	Status         QualificationStatus `json:"status"`
	Version        int                 `json:"version"`
	Variables      ProcedureVariables  `json:"variables"`
	Evidence       []TestEvidence      `json:"evidence"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	FailureReasons []string            `json:"failure_reasons,omitempty"`
}
type TestEvidence struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Result    Fixed      `json:"result"`
	Unit      string     `json:"unit"`
	Passed    bool       `json:"passed"`
	Source    string     `json:"source"`
	TestedAt  time.Time  `json:"tested_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

func (e TestEvidence) Active(at time.Time) bool {
	return e.RevokedAt == nil && (e.ExpiresAt == nil || !at.After(*e.ExpiresAt))
}

type CoverageRange struct {
	Variable    string    `json:"variable"`
	Numeric     *Interval `json:"numeric,omitempty"`
	Values      []string  `json:"values,omitempty"`
	Clause      string    `json:"clause"`
	InputDigest string    `json:"input_digest"`
}
type ProcedureSpecification struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Revisions           []string `json:"revisions"`
	PublishedRevisionID string   `json:"published_revision_id,omitempty"`
	Version             int      `json:"version"`
}
type ProcedureRevision struct {
	ID               string             `json:"id"`
	ProcedureID      string             `json:"procedure_id"`
	Number           int                `json:"number"`
	Status           RevisionStatus     `json:"status"`
	Version          int                `json:"version"`
	RuleSetID        string             `json:"rule_set_id"`
	QualificationIDs []string           `json:"qualification_ids"`
	Variables        ProcedureVariables `json:"variables"`
	Coverage         []CoverageRange    `json:"coverage,omitempty"`
	CoverageGaps     []string           `json:"coverage_gaps,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
	PublishedAt      *time.Time         `json:"published_at,omitempty"`
}
type JointRequirement struct {
	ID            string             `json:"id"`
	Version       int                `json:"version"`
	Reference     string             `json:"reference"`
	Variables     ProcedureVariables `json:"variables"`
	CreatedAt     time.Time          `json:"created_at"`
	AssessmentIDs []string           `json:"assessment_ids,omitempty"`
}
type Difference struct {
	Variable string        `json:"variable"`
	Expected string        `json:"expected"`
	Actual   string        `json:"actual"`
	Class    VariableClass `json:"class,omitempty"`
	Clause   string        `json:"clause,omitempty"`
	Severity string        `json:"severity"`
	Message  string        `json:"message"`
}
type ApplicabilityAssessment struct {
	ID            string       `json:"id"`
	RequirementID string       `json:"requirement_id"`
	RevisionID    string       `json:"revision_id"`
	RuleSetID     string       `json:"rule_set_id"`
	Conclusion    string       `json:"conclusion"`
	Differences   []Difference `json:"differences"`
	Conditions    []string     `json:"conditions,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	Stale         bool         `json:"stale"`
	StaleReason   string       `json:"stale_reason,omitempty"`
}
type ChangeReview struct {
	ID             string       `json:"id"`
	FromRevisionID string       `json:"from_revision_id"`
	ToRevisionID   string       `json:"to_revision_id"`
	RuleSetID      string       `json:"rule_set_id"`
	Status         ReviewStatus `json:"status"`
	Version        int          `json:"version"`
	Differences    []Difference `json:"differences"`
	Blocking       []string     `json:"blocking,omitempty"`
	Decision       string       `json:"decision,omitempty"`
	DecidedAt      *time.Time   `json:"decided_at,omitempty"`
}
type ReviewTask struct {
	ID             string     `json:"id"`
	Key            string     `json:"key"`
	Reason         string     `json:"reason"`
	ObjectType     string     `json:"object_type"`
	ObjectID       string     `json:"object_id"`
	TriggerVersion string     `json:"trigger_version"`
	Status         TaskStatus `json:"status"`
	Attempts       int        `json:"attempts"`
	MaxAttempts    int        `json:"max_attempts"`
	NextRunAt      time.Time  `json:"next_run_at"`
	LastError      string     `json:"last_error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}
type AuditEvent struct {
	Sequence     uint64    `json:"sequence"`
	Time         time.Time `json:"time"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	ObjectType   string    `json:"object_type"`
	ObjectID     string    `json:"object_id"`
	BeforeDigest string    `json:"before_digest"`
	AfterDigest  string    `json:"after_digest"`
	PreviousHash string    `json:"previous_hash"`
	Hash         string    `json:"hash"`
}
type IdempotencyRecord struct {
	Key           string          `json:"key"`
	RequestDigest string          `json:"request_digest"`
	Status        int             `json:"status"`
	Response      json.RawMessage `json:"response"`
	CreatedAt     time.Time       `json:"created_at"`
}

type State struct {
	SchemaVersion  int                                `json:"schema_version"`
	Sequence       uint64                             `json:"sequence"`
	Rules          map[string]RuleSet                 `json:"rules"`
	Qualifications map[string]QualificationRecord     `json:"qualifications"`
	Procedures     map[string]ProcedureSpecification  `json:"procedures"`
	Revisions      map[string]ProcedureRevision       `json:"revisions"`
	Requirements   map[string]JointRequirement        `json:"requirements"`
	Assessments    map[string]ApplicabilityAssessment `json:"assessments"`
	ChangeReviews  map[string]ChangeReview            `json:"change_reviews"`
	Tasks          map[string]ReviewTask              `json:"tasks"`
	Audit          []AuditEvent                       `json:"audit"`
	Idempotency    map[string]IdempotencyRecord       `json:"idempotency"`
	Digest         string                             `json:"digest"`
}

func NewState() State {
	return State{SchemaVersion: SchemaVersion, Rules: map[string]RuleSet{}, Qualifications: map[string]QualificationRecord{}, Procedures: map[string]ProcedureSpecification{}, Revisions: map[string]ProcedureRevision{}, Requirements: map[string]JointRequirement{}, Assessments: map[string]ApplicabilityAssessment{}, ChangeReviews: map[string]ChangeReview{}, Tasks: map[string]ReviewTask{}, Idempotency: map[string]IdempotencyRecord{}}
}
func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func CanonicalStrings(v []string) []string {
	r := append([]string(nil), v...)
	for i := range r {
		r[i] = strings.ToUpper(strings.TrimSpace(r[i]))
	}
	sort.Strings(r)
	return r
}
func EqualStrings(a, b []string) bool {
	a = CanonicalStrings(a)
	b = CanonicalStrings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func NewID(prefix string, seq uint64) string { return fmt.Sprintf("%s-%08d", prefix, seq) }
func max[T ~int64](a, b T) T {
	if a > b {
		return a
	}
	return b
}
func min[T ~int64](a, b T) T {
	if a < b {
		return a
	}
	return b
}
