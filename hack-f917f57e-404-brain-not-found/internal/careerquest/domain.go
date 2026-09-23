package careerquest

import (
	"encoding/json"
	"fmt"
)

// These are logical components in one application, not separate agents.
type Employee struct {
	Department        string         `json:"department,omitempty"`
	ManagerID         string         `json:"manager_id,omitempty"`
	HireDate          string         `json:"hire_date,omitempty"`
	WorkFormat        string         `json:"work_format,omitempty"`
	PreferredLanguage string         `json:"preferred_language,omitempty"`
	LastReviewDate    string         `json:"last_review_date,omitempty"`
	GradeLabel        string         `json:"grade_label,omitempty"`
	CareerGoal        *CareerGoal    `json:"career_goal,omitempty"`
	AssessmentSkills  map[string]int `json:"assessment_skills,omitempty"`
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Role              string         `json:"role"`
	Grade             int            `json:"grade"`
	TenureMonths      int            `json:"tenure_months"`
	Skills            map[string]int `json:"skills"`
	Version           int            `json:"version"`
}

type Requirement struct {
	Role           string         `json:"role"`
	Grade          int            `json:"grade"`
	Skills         map[string]int `json:"skills"`
	CriticalSkills []string       `json:"critical_skills,omitempty"`
}

type Growth struct {
	Gain     int `json:"gain"`
	MaxLevel int `json:"max_level"`
}

type Activity struct {
	RuleVersion        string            `json:"rule_version,omitempty"`
	Mandatory          bool              `json:"mandatory,omitempty"`
	Type               string            `json:"type,omitempty"`
	DurationHours      float64           `json:"duration_hours,omitempty"`
	TargetGrades       []int             `json:"target_grades,omitempty"`
	SkillPrerequisites map[string]int    `json:"skill_prerequisites,omitempty"`
	UpcomingSessions   []string          `json:"upcoming_sessions,omitempty"`
	ID                 string            `json:"id"`
	Title              string            `json:"title"`
	Description        string            `json:"description"`
	Format             string            `json:"format"`
	Hours              int               `json:"hours"`
	Roles              []string          `json:"roles"`
	MinGrade           int               `json:"min_grade"`
	MaxGrade           int               `json:"max_grade"`
	Repeatable         bool              `json:"repeatable"`
	Prerequisites      []string          `json:"prerequisites"`
	Growth             map[string]Growth `json:"growth"`
}

type Change struct {
	Skill  string `json:"skill"`
	Before int    `json:"before"`
	After  int    `json:"after"`
}

type Participation struct {
	RecordID       string            `json:"record_id,omitempty"`
	DueDate        string            `json:"due_date,omitempty"`
	AssignedBy     string            `json:"assigned_by,omitempty"`
	CompletionPct  int               `json:"completion_pct,omitempty"`
	Score          *int              `json:"score,omitempty"`
	FeedbackRating *int              `json:"feedback_rating,omitempty"`
	EmployeeID     string            `json:"employee_id"`
	ActivityID     string            `json:"activity_id"`
	Status         string            `json:"status"`
	Date           string            `json:"date"`
	RequestID      string            `json:"request_id,omitempty"`
	Changes        []Change          `json:"changes,omitempty"`
	Projections    []SkillProjection `json:"projections,omitempty"`
	RuleVersion    string            `json:"rule_version,omitempty"`
}

type Recommendation struct {
	Activity Activity `json:"activity"`
	Changes  []Change `json:"changes"`
	Closure  int      `json:"closure"`
	Score    int      `json:"score"`
	Reasons  []string `json:"reasons"`
}

type RecommendationSet struct {
	Version int              `json:"version"`
	Source  string           `json:"source"`
	Items   []Recommendation `json:"items"`
	Message string           `json:"message"`
	// Nil for legacy/prototype results; absence never means validated evidence.
	Evidence *RecommendationEvidence `json:"evidence,omitempty"`
}

type Dataset struct {
	Source          string                       `json:"source,omitempty"`
	SkillCatalog    []SkillDefinition            `json:"skill_catalog,omitempty"`
	DataEpoch       uint64                       `json:"data_epoch,omitempty"`
	ReferenceDate   string                       `json:"reference_date,omitempty"`
	HistoryStart    string                       `json:"history_start,omitempty"`
	HistoryEnd      string                       `json:"history_end,omitempty"`
	Employees       []Employee                   `json:"employees"`
	Requirements    []Requirement                `json:"requirements"`
	Activities      []Activity                   `json:"activities"`
	History         []Participation              `json:"history"`
	Recommendations map[string]RecommendationSet `json:"recommendations,omitempty"`
}

type SkillGap struct {
	Skill                string
	Current, Target, Gap int
	Assessed             bool
}

type EmployeeContext struct {
	Employee                               Employee
	Gaps                                   []SkillGap
	History                                []Participation
	HasTarget                              bool
	Progress, Missing                      int
	ProfileSkills                          []ProfileSkill
	ProgressDefined                        bool
	ProgressNumerator, ProgressDenominator int
	ProgressTerms                          []ProgressTerm
	ProgressState                          string
}

// Shared capped growth rule. Version/official normalization is chosen at import;
// generic unknown assessments stay unknown. Existing levels never decrease.
func grow(level int, rule Growth) int {
	return max(level, min(level+rule.Gain, rule.MaxLevel))
}

func cloneDataset(d Dataset) Dataset {
	b, _ := json.Marshal(d)
	var copy Dataset
	_ = json.Unmarshal(b, &copy)
	if copy.Recommendations == nil {
		copy.Recommendations = map[string]RecommendationSet{}
	}
	return copy
}

func findEmployee(d Dataset, id string) (Employee, bool) {
	for _, e := range d.Employees {
		if e.ID == id {
			return e, true
		}
	}
	return Employee{}, false
}

func findActivity(d Dataset, id string) (Activity, bool) {
	for _, a := range d.Activities {
		if a.ID == id {
			return a, true
		}
	}
	return Activity{}, false
}

func contextFor(d Dataset, e Employee) EmployeeContext {
	return profileContext(d, e)
}

func eligible(d Dataset, e Employee, a Activity) bool {
	if len(a.TargetGrades) > 0 {
		matched := false
		for _, grade := range a.TargetGrades {
			if grade == e.Grade {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	for skill, minimum := range a.SkillPrerequisites {
		if level, assessed := e.Skills[skill]; !assessed || level < minimum {
			return false
		}
	}
	if e.Grade < a.MinGrade || e.Grade > a.MaxGrade {
		return false
	}
	roleOK := len(a.Roles) == 0
	for _, role := range a.Roles {
		if role == e.Role {
			roleOK = true
		}
	}
	if !roleOK {
		return false
	}
	completed := map[string]bool{}
	for _, p := range d.History {
		if p.EmployeeID == e.ID && p.Status == "completed" {
			completed[p.ActivityID] = true
		}
	}
	if completed[a.ID] && !a.Repeatable {
		return false
	}
	for _, id := range a.Prerequisites {
		if !completed[id] {
			return false
		}
	}
	return true
}

func simulate(e Employee, a Activity, gaps []SkillGap) ([]Change, int) {
	changes := []Change{}
	projections, closure := projectActivity(e, a, gaps)
	for _, projection := range projections {
		if projection.Effect != nil && projection.AppliedGain > 0 {
			changes = append(changes, *projection.Effect)
		}
	}
	return changes, closure
}

func candidates(d Dataset, e Employee) []Recommendation {
	evaluations := evaluateCandidatesAt(d, e, datasetReferenceTime(d))
	items := make([]Recommendation, 0, len(evaluations))
	for _, evaluation := range evaluations {
		items = append(items, recommendationFor(d, e, evaluation))
	}
	return items
}

func recommend(d Dataset, e Employee) RecommendationSet {
	return recommendAt(d, e, datasetReferenceTime(d))
}

func validateDataset(d Dataset) error {
	if len(d.Employees) == 0 || len(d.Activities) == 0 || len(d.Requirements) == 0 {
		return fmt.Errorf("employees, activities, and requirements must not be empty")
	}
	if len(d.Employees) > 1000 || len(d.Activities) > 1000 || len(d.History) > 20000 {
		return fmt.Errorf("dataset exceeds supported limits")
	}
	employees, activities, requirements := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range d.Employees {
		if e.ID == "" || e.ID == "hr" || e.Name == "" || e.Role == "" || e.Grade < 1 || e.Grade > 9 || e.TenureMonths < 0 || employees[e.ID] {
			return fmt.Errorf("invalid or duplicate employee: %s", e.ID)
		}
		employees[e.ID] = true
		for skill, level := range e.Skills {
			if skill == "" || level < 0 || level > 10 {
				return fmt.Errorf("invalid assessment for %s", e.ID)
			}
		}
	}
	for _, r := range d.Requirements {
		key := fmt.Sprintf("%s:%d", r.Role, r.Grade)
		if r.Role == "" || r.Grade < 1 || r.Grade > 10 || len(r.Skills) == 0 || requirements[key] {
			return fmt.Errorf("invalid or duplicate grade requirements: %s", key)
		}
		requirements[key] = true
		for skill, level := range r.Skills {
			if skill == "" || level < 1 || level > 10 {
				return fmt.Errorf("invalid required skill: %s", skill)
			}
		}
		criticalSeen := map[string]bool{}
		for _, skill := range r.CriticalSkills {
			if _, exists := r.Skills[skill]; !exists || criticalSeen[skill] {
				return fmt.Errorf("invalid or duplicate critical skill: %s", skill)
			}
			criticalSeen[skill] = true
		}
	}
	for _, a := range d.Activities {
		if a.ID == "" || a.Title == "" || a.Hours < 1 || a.MinGrade < 1 || a.MaxGrade < a.MinGrade || a.MaxGrade > 9 || (len(a.Growth) == 0 && !a.Mandatory) || activities[a.ID] {
			return fmt.Errorf("invalid or duplicate activity: %s", a.ID)
		}
		activities[a.ID] = true
		for skill, rule := range a.Growth {
			if skill == "" || rule.Gain < 1 || rule.Gain > 10 || rule.MaxLevel < 1 || rule.MaxLevel > 10 {
				return fmt.Errorf("invalid growth rule: %s", a.ID)
			}
		}
	}
	for _, a := range d.Activities {
		for _, id := range a.Prerequisites {
			if !activities[id] || id == a.ID {
				return fmt.Errorf("invalid prerequisite for %s", a.ID)
			}
		}
	}
	requests := map[string]bool{}
	for _, p := range d.History {
		if !employees[p.EmployeeID] || !activities[p.ActivityID] {
			return fmt.Errorf("history references an unknown employee or activity")
		}
		switch p.Status {
		case "completed", "registered", "no_show", "refused", "in_progress", "dropped", "declined", "overdue":
		default:
			return fmt.Errorf("unknown participation status: %s", p.Status)
		}
		if p.RequestID != "" {
			key := p.EmployeeID + ":" + p.RequestID
			if requests[key] {
				return fmt.Errorf("duplicate completion request")
			}
			requests[key] = true
		}
	}
	return nil
}

type GapCount struct {
	Skill                      string
	Employees, Points, Missing int
}

type Coverage struct{ Name, State string }
type ParticipationCount struct {
	InProgress, Dropped, Declined, Overdue int
	Title                                  string
	Completed, Registered, NoShow, Refused int
}

type HRSummary struct {
	Engagement                                 []EngagementRow
	AsOfDate, Window30Start, Window90Start     string
	Employees, Suggested, Completions, Missing int
	Gaps                                       []GapCount
	Coverage                                   []Coverage
	Participation                              []ParticipationCount
}

func hrSummary(d Dataset) HRSummary {
	reference := datasetReferenceTime(d)
	s := hrSummaryAt(d, reference)
	s.AsOfDate = referenceDay(reference).Format("2006-01-02")
	s.Window30Start = referenceDay(reference).AddDate(0, 0, -29).Format("2006-01-02")
	s.Window90Start = referenceDay(reference).AddDate(0, 0, -89).Format("2006-01-02")
	s.Engagement = engagementRows(d, reference)
	for i := range s.Engagement {
		s.Engagement[i].RecommendationState = s.Coverage[i].State
	}
	return s
}
