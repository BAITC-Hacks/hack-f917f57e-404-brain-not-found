package careerquest

import "time"

// Shared recommendation and progress contracts.
// An absent Evidence value is never proof of validation.
const StandardGrowthRuleVersion = "prototype-capped-v1"

// HistoryRecordReference identifies evidence within a particular dataset revision.
// Official imports supply RecordID. Standard JSON history uses HistoryIndex
// (zero based, including index 0); an index alone is not stable across imports.
type HistoryRecordReference struct {
	DatasetRevision string `json:"dataset_revision"`
	RecordID        string `json:"record_id,omitempty"`
	HistoryIndex    *int   `json:"history_index,omitempty"`
}

// ObservationWindow includes both endpoints. Nil bounds mean unknown, not an
// empty history window. ReferenceDate must be supplied by the dataset/clock owner.
type ObservationWindow struct {
	From          *time.Time `json:"from,omitempty"`
	Through       *time.Time `json:"through,omitempty"`
	ReferenceDate time.Time  `json:"reference_date"`
}

type ParticipationEvidence struct {
	Record        HistoryRecordReference `json:"record"`
	Participation Participation          `json:"participation"`
	// Exact activity matches use "exact_activity_id". Related matches name
	// the actual relationship and its supporting skill/type/category IDs.
	RelationshipBasis []string `json:"relationship_basis"`
	ExclusionReason   string   `json:"exclusion_reason,omitempty"`
}

type HistoryEvidence struct {
	Exact           []ParticipationEvidence `json:"exact"`
	Related         []ParticipationEvidence `json:"related"`
	Excluded        []ParticipationEvidence `json:"excluded,omitempty"`
	ExactOutcomes   map[string]int          `json:"exact_outcomes"`
	RelatedOutcomes map[string]int          `json:"related_outcomes"`
	Window          ObservationWindow       `json:"window"`
	MissingData     []string                `json:"missing_data"`
	// Earliest/latest records observed are not proof that the full window
	// was collected. Keep that distinction for sparse/imported histories.
	ObservedFrom    *time.Time `json:"observed_from,omitempty"`
	ObservedThrough *time.Time `json:"observed_through,omitempty"`
}

// SkillProjection reuses the existing assessment, growth rule and awarded-change
// types. Assessment.Current is meaningful only when Assessed is true. Target/Gap
// are meaningful only when HasTarget is true. Effect is nil for unknown results;
// an assessed, capped skill can have an effect with Before == After.
// Derive projections through the shared grow/simulate calculator.
type SkillProjection struct {
	Assessment   SkillGap `json:"assessment"`
	HasTarget    bool     `json:"has_target"`
	Advertised   Growth   `json:"advertised"`
	Effect       *Change  `json:"effect,omitempty"`
	AppliedGain  int      `json:"applied_gain"`
	RemainingGap *int     `json:"remaining_gap,omitempty"`
	RuleVersion  string   `json:"rule_version"`
}

type CandidateComparison struct {
	AlternativeActivityID string                   `json:"alternative_activity_id"`
	Reasons               []string                 `json:"reasons"`
	HistoryRecords        []HistoryRecordReference `json:"history_records"`
	SelectedBenefits      CandidateBenefits        `json:"selected_benefits"`
	AlternativeBenefits   CandidateBenefits        `json:"alternative_benefits"`
}

// Integer score components are application policy, never official case weights.
type CandidateBenefits struct {
	GapClosure         int `json:"gap_closure"`
	CriticalGapClosure int `json:"critical_gap_closure"`
	HistoryPenalty     int `json:"history_penalty"`
	DurationPenalty    int `json:"duration_penalty"`
	Score              int `json:"score"`
}

type CandidateEvaluation struct {
	ActivityID         string                `json:"activity_id"`
	Eligible           bool                  `json:"eligible"`
	EligibilityReasons []string              `json:"eligibility_reasons"`
	Projections        []SkillProjection     `json:"projections"`
	History            HistoryEvidence       `json:"history"`
	Limitations        []string              `json:"limitations"`
	Comparisons        []CandidateComparison `json:"comparisons"`
	EmployeeID         string                `json:"employee_id"`
	Step               int                   `json:"step"`
	PriorActivityIDs   []string              `json:"prior_activity_ids"`
	Selected           bool                  `json:"selected"`
	Benefits           CandidateBenefits     `json:"benefits"`
}

type RecommendationValidation string

const (
	RecommendationNotValidated RecommendationValidation = "not_validated"
	RecommendationValidated    RecommendationValidation = "validated"
	RecommendationRejected     RecommendationValidation = "rejected"
)

type RecommendationEvidence struct {
	Model         string     `json:"model,omitempty"`
	PromptVersion string     `json:"prompt_version,omitempty"`
	ResponseID    string     `json:"response_id,omitempty"`
	GeneratedAt   *time.Time `json:"generated_at,omitempty"`
	// Version on RecommendationSet remains the employee version. This
	// revision must also change when catalog, requirements or history change.
	DatasetRevision   string                   `json:"dataset_revision"`
	Provider          string                   `json:"provider,omitempty"`
	Validation        RecommendationValidation `json:"validation"`
	Limitations       []string                 `json:"limitations"`
	Candidates        []CandidateEvaluation    `json:"candidates"`
	EmployeeID        string                   `json:"employee_id"`
	ReferenceDate     time.Time                `json:"reference_date"`
	PolicyVersion     string                   `json:"policy_version"`
	GrowthRuleVersion string                   `json:"growth_rule_version"`
}

// Keep one recommendation result model and its existing ordered Items/Changes,
// Reasons, Source and Version fields. Do not introduce a second persisted result.
type RecommendationResult = RecommendationSet

type EngagementSummary struct {
	EmployeeID      string                   `json:"employee_id"`
	LastCompletion  *time.Time               `json:"last_completion,omitempty"`
	RecentOutcomes  map[string]int           `json:"recent_outcomes"`
	Window          ObservationWindow        `json:"window"`
	ObservedFrom    *time.Time               `json:"observed_from,omitempty"`
	ObservedThrough *time.Time               `json:"observed_through,omitempty"`
	MissingData     []string                 `json:"missing_data"`
	Records         []HistoryRecordReference `json:"records"`
}
