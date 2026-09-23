package careerquest

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Native Halyk v1 boundary. Only synthetic tests and schema definitions belong in
// the repository; authorized source archives and imported state remain local.
type SkillDefinition struct {
	ID          string `json:"skill_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

type CareerGoal struct {
	TargetRole  string `json:"target_role"`
	TargetGrade string `json:"target_grade"`
}

type ImportSummary struct {
	Employees, Activities, Skills, History, AppliedCompletions int
	AddedEmployees, AddedHistory                               int
	ReferenceDate                                              string
}

type officialMeta struct {
	Dataset  string `json:"dataset"`
	Version  string `json:"version"`
	AsOfDate string `json:"as_of_date"`
}

type officialEmployee struct {
	ID                string         `json:"employee_id"`
	Name              string         `json:"full_name"`
	Department        string         `json:"department"`
	Role              string         `json:"role"`
	Grade             string         `json:"grade"`
	ManagerID         *string        `json:"manager_id"`
	HireDate          string         `json:"hire_date"`
	TenureMonths      int            `json:"tenure_months"`
	WorkFormat        string         `json:"work_format"`
	PreferredLanguage string         `json:"preferred_language"`
	CareerGoal        *CareerGoal    `json:"career_goal"`
	Skills            map[string]int `json:"skills"`
	LastReviewDate    string         `json:"last_review_date"`
}

type officialEmployees struct {
	Meta      officialMeta       `json:"meta"`
	Employees []officialEmployee `json:"employees"`
}

type officialActivity struct {
	ID            string   `json:"event_id"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Type          string   `json:"type"`
	Format        string   `json:"format"`
	DurationHours float64  `json:"duration_hours"`
	Mandatory     bool     `json:"mandatory"`
	Roles         []string `json:"target_roles"`
	Grades        []string `json:"target_grades"`
	Develops      []struct {
		Skill    string `json:"skill_id"`
		Gain     int    `json:"gain"`
		MaxLevel int    `json:"max_level"`
	} `json:"develops_skills"`
	Prerequisites    map[string]int `json:"prerequisites"`
	UpcomingSessions []string       `json:"upcoming_sessions"`
}

type officialEvents struct {
	Meta   officialMeta       `json:"meta"`
	Events []officialActivity `json:"events"`
}

type officialSkills struct {
	Meta             officialMeta      `json:"meta"`
	ProficiencyScale map[string]string `json:"proficiency_scale"`
	Skills           []SkillDefinition `json:"skills"`
	Profiles         []struct {
		Role     string         `json:"role"`
		Grade    string         `json:"grade"`
		Required map[string]int `json:"required_skills"`
		Critical []string       `json:"critical_skills"`
	} `json:"role_profiles"`
}

func officialGrade(label string) (int, error) {
	for i, value := range []string{"Junior", "Middle", "Senior", "Lead"} {
		if label == value {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("unsupported grade %q; expected Junior/Middle/Senior/Lead", label)
}

func decodeOfficialJSON(name string, b []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("%s: exactly one JSON object required", name)
	}
	return nil
}

func officialZipFiles(reader io.ReaderAt, size int64) (map[string][]byte, error) {
	if size <= 0 || size > 20<<20 {
		return nil, fmt.Errorf("ZIP must be between 1 byte and 20 MB")
	}
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, fmt.Errorf("invalid ZIP: %w", err)
	}
	files := map[string][]byte{}
	total := int64(0)
	for _, entry := range archive.File {
		name := path.Base(strings.ReplaceAll(entry.Name, "\\", "/"))
		if strings.Contains(entry.Name, "__MACOSX") || strings.HasPrefix(name, ".") {
			continue
		}
		switch name {
		case "employees.json", "events.json", "skills.json", "activity_history.csv":
		default:
			continue
		}
		clean := strings.ReplaceAll(entry.Name, "\\", "/")
		if strings.HasPrefix(clean, "/") || strings.Contains(clean, ":") || containsString(strings.Split(clean, "/"), "..") {
			return nil, fmt.Errorf("ZIP contains unsafe source path")
		}
		if _, exists := files[name]; exists {
			return nil, fmt.Errorf("ZIP contains duplicate %s", name)
		}
		if entry.UncompressedSize64 > 20<<20 {
			return nil, fmt.Errorf("%s exceeds 20 MB", name)
		}
		r, err := entry.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(r, (20<<20)+1))
		_ = r.Close()
		if err != nil {
			return nil, err
		}
		total += int64(len(b))
		if total > 20<<20 {
			return nil, fmt.Errorf("expanded files exceed 20 MB")
		}
		files[name] = b
	}
	return files, nil
}

func validateOfficialMeta(meta officialMeta, expected string) error {
	if meta.Dataset != "Career Quest" || meta.Version != "1.0" {
		return fmt.Errorf("meta: expected Career Quest schema version 1.0")
	}
	if _, err := time.Parse("2006-01-02", meta.AsOfDate); err != nil {
		return fmt.Errorf("meta.as_of_date: invalid ISO date")
	}
	if expected != "" && meta.AsOfDate != expected {
		return fmt.Errorf("meta.as_of_date differs from current catalogue snapshot (%s)", expected)
	}
	return nil
}

func exactOfficialDate(value, field string) (time.Time, error) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: expected YYYY-MM-DD", field)
	}
	return date, nil
}

func decodeOfficialZip(reader io.ReaderAt, size int64) (Dataset, ImportSummary, error) {
	files, err := officialZipFiles(reader, size)
	if err != nil {
		return Dataset{}, ImportSummary{}, err
	}
	for _, name := range []string{"employees.json", "events.json", "skills.json", "activity_history.csv"} {
		if _, ok := files[name]; !ok {
			return Dataset{}, ImportSummary{}, fmt.Errorf("missing %s", name)
		}
	}
	var skills officialSkills
	var events officialEvents
	var employees officialEmployees
	if err = decodeOfficialJSON("skills.json", files["skills.json"], &skills); err != nil {
		return Dataset{}, ImportSummary{}, err
	}
	if err = decodeOfficialJSON("events.json", files["events.json"], &events); err != nil {
		return Dataset{}, ImportSummary{}, err
	}
	if err = decodeOfficialJSON("employees.json", files["employees.json"], &employees); err != nil {
		return Dataset{}, ImportSummary{}, err
	}
	for _, meta := range []officialMeta{skills.Meta, events.Meta, employees.Meta} {
		if err = validateOfficialMeta(meta, skills.Meta.AsOfDate); err != nil {
			return Dataset{}, ImportSummary{}, err
		}
	}
	d := Dataset{Source: "halyk-v1", ReferenceDate: skills.Meta.AsOfDate, HistoryStart: "2024-10-01", HistoryEnd: "2026-09-30", Recommendations: map[string]RecommendationSet{}, SkillCatalog: skills.Skills}
	// The supplied README establishes this collection window only for v1's
	// supplied snapshot. Later employee snapshots do not inherit guessed coverage.
	if d.ReferenceDate != "2026-10-01" {
		d.HistoryStart = ""
		d.HistoryEnd = ""
	}
	skillIDs := map[string]bool{}
	for i, skill := range skills.Skills {
		if skill.ID == "" || skill.Name == "" || skillIDs[skill.ID] || (skill.Type != "hard" && skill.Type != "soft") {
			return Dataset{}, ImportSummary{}, fmt.Errorf("skills.json skills[%d]: invalid/duplicate skill", i)
		}
		skillIDs[skill.ID] = true
	}
	if len(skillIDs) == 0 {
		return Dataset{}, ImportSummary{}, fmt.Errorf("skills.json: no skills")
	}
	for i := 0; i <= 5; i++ {
		if skills.ProficiencyScale[strconv.Itoa(i)] == "" {
			return Dataset{}, ImportSummary{}, fmt.Errorf("proficiency_scale: level %d missing", i)
		}
	}
	roles := map[string]bool{}
	for i, profile := range skills.Profiles {
		grade, err := officialGrade(profile.Grade)
		if err != nil {
			return Dataset{}, ImportSummary{}, fmt.Errorf("role_profiles[%d]: %w", i, err)
		}
		for skill, level := range profile.Required {
			if !skillIDs[skill] || level < 1 || level > 5 {
				return Dataset{}, ImportSummary{}, fmt.Errorf("role_profiles[%d].required_skills[%s]: invalid skill/level", i, skill)
			}
		}
		d.Requirements = append(d.Requirements, Requirement{Role: profile.Role, Grade: grade, Skills: profile.Required, CriticalSkills: profile.Critical})
		roles[profile.Role] = true
	}
	for _, profile := range d.Requirements {
		for _, previous := range d.Requirements {
			if previous.Role == profile.Role && previous.Grade == profile.Grade-1 {
				for skill, level := range previous.Skills {
					if profile.Skills[skill] < level {
						return Dataset{}, ImportSummary{}, fmt.Errorf("role_profiles: requirements decrease for %s/%s", profile.Role, skill)
					}
				}
			}
		}
	}
	for i, event := range events.Events {
		if event.DurationHours <= 0 || event.DurationHours > 10000 {
			return Dataset{}, ImportSummary{}, fmt.Errorf("events[%d].duration_hours: must be positive and <=10000", i)
		}
		if !containsString([]string{"compliance", "onboarding", "course", "workshop", "mentoring", "certification", "meetup"}, event.Type) || !containsString([]string{"online", "offline", "self_paced"}, event.Format) {
			return Dataset{}, ImportSummary{}, fmt.Errorf("events[%d]: unsupported type or format", i)
		}
		a := Activity{ID: event.ID, Title: event.Title, Description: event.Description, Type: event.Type, Format: event.Format, Hours: int(math.Ceil(event.DurationHours)), DurationHours: event.DurationHours, Mandatory: event.Mandatory, Roles: event.Roles, MinGrade: 4, MaxGrade: 1, Repeatable: event.ID == "EV_036", SkillPrerequisites: event.Prerequisites, UpcomingSessions: event.UpcomingSessions, Growth: map[string]Growth{}, RuleVersion: OfficialGrowthRuleVersion}
		for _, role := range a.Roles {
			if !roles[role] {
				return Dataset{}, ImportSummary{}, fmt.Errorf("events[%d].target_roles: unknown role", i)
			}
		}
		if len(event.Grades) == 0 {
			return Dataset{}, ImportSummary{}, fmt.Errorf("events[%d].target_grades: empty", i)
		}
		for _, label := range event.Grades {
			grade, err := officialGrade(label)
			if err != nil {
				return Dataset{}, ImportSummary{}, fmt.Errorf("events[%d].target_grades: %w", i, err)
			}
			a.TargetGrades = append(a.TargetGrades, grade)
			a.MinGrade = min(a.MinGrade, grade)
			a.MaxGrade = max(a.MaxGrade, grade)
		}
		for _, development := range event.Develops {
			if !skillIDs[development.Skill] || development.Gain < 1 || development.Gain > 5 || development.MaxLevel < 1 || development.MaxLevel > 5 {
				return Dataset{}, ImportSummary{}, fmt.Errorf("events[%d].develops_skills: invalid skill/gain/cap", i)
			}
			if _, exists := a.Growth[development.Skill]; exists {
				return Dataset{}, ImportSummary{}, fmt.Errorf("events[%d].develops_skills: duplicate skill", i)
			}
			a.Growth[development.Skill] = Growth{Gain: development.Gain, MaxLevel: development.MaxLevel}
		}
		for skill, minimum := range a.SkillPrerequisites {
			if !skillIDs[skill] || minimum < 0 || minimum > 5 {
				return Dataset{}, ImportSummary{}, fmt.Errorf("events[%d].prerequisites: invalid skill/level", i)
			}
		}
		for _, date := range a.UpcomingSessions {
			parsed, err := exactOfficialDate(date, fmt.Sprintf("events[%d].upcoming_sessions", i))
			if err != nil {
				return Dataset{}, ImportSummary{}, err
			}
			if parsed.Before(datasetReferenceTime(d)) {
				return Dataset{}, ImportSummary{}, fmt.Errorf("events[%d].upcoming_sessions: date precedes snapshot", i)
			}
		}
		d.Activities = append(d.Activities, a)
	}
	if err = appendOfficialEmployees(&d, employees.Employees, false); err != nil {
		return Dataset{}, ImportSummary{}, err
	}
	history, err := decodeOfficialHistory(files["activity_history.csv"], d)
	if err != nil {
		return Dataset{}, ImportSummary{}, err
	}
	d.History = history
	applied := replayOfficialCompletions(&d, history)
	if err = validateDataset(d); err != nil {
		return Dataset{}, ImportSummary{}, fmt.Errorf("official validation: %w", err)
	}
	return d, officialSummary(d, applied, len(d.Employees), len(d.History)), nil
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func officialSummary(d Dataset, applied, employees, history int) ImportSummary {
	return ImportSummary{Employees: len(d.Employees), Activities: len(d.Activities), Skills: len(d.SkillCatalog), History: len(d.History), AppliedCompletions: applied, AddedEmployees: employees, AddedHistory: history, ReferenceDate: d.ReferenceDate}
}

func appendOfficialEmployees(d *Dataset, employees []officialEmployee, merge bool) error {
	skills := map[string]bool{}
	for _, skill := range d.SkillCatalog {
		skills[skill.ID] = true
	}
	existing := map[string]int{}
	for i, e := range d.Employees {
		existing[e.ID] = i
	}
	seen := map[string]bool{}
	for i, source := range employees {
		prefix := fmt.Sprintf("employees.json employees[%d]", i)
		if source.ID == "" || source.ID == "hr" || source.Name == "" || source.Department == "" || seen[source.ID] {
			return fmt.Errorf("%s: required identity/department missing or ID duplicated", prefix)
		}
		seen[source.ID] = true
		grade, err := officialGrade(source.Grade)
		if err != nil {
			return fmt.Errorf("%s.grade: %w", prefix, err)
		}
		knownRole := false
		for _, target := range d.Requirements {
			if target.Role == source.Role && target.Grade == grade {
				knownRole = true
			}
		}
		if !knownRole {
			return fmt.Errorf("%s.role/grade: missing role profile", prefix)
		}
		hire, err := exactOfficialDate(source.HireDate, prefix+".hire_date")
		if err != nil {
			return err
		}
		review, err := exactOfficialDate(source.LastReviewDate, prefix+".last_review_date")
		if err != nil {
			return err
		}
		if hire.After(datasetReferenceTime(*d)) || review.After(datasetReferenceTime(*d)) || source.TenureMonths < 0 {
			return fmt.Errorf("%s: date after snapshot or negative tenure", prefix)
		}
		if !containsString([]string{"office", "hybrid", "remote"}, source.WorkFormat) || !containsString([]string{"kk", "ru", "en"}, source.PreferredLanguage) {
			return fmt.Errorf("%s: unsupported work_format/preferred_language", prefix)
		}
		if source.CareerGoal != nil {
			targetGrade, err := officialGrade(source.CareerGoal.TargetGrade)
			if err != nil {
				return fmt.Errorf("%s.career_goal: %w", prefix, err)
			}
			found := false
			for _, r := range d.Requirements {
				if r.Role == source.CareerGoal.TargetRole && r.Grade == targetGrade {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("%s.career_goal: unknown role/grade", prefix)
			}
		}
		levels := map[string]int{}
		for skill := range skills {
			levels[skill] = 0
		}
		for skill, level := range source.Skills {
			if !skills[skill] || level < 0 || level > 5 {
				return fmt.Errorf("%s.skills[%s]: unknown skill or level outside 0–5", prefix, skill)
			}
			levels[skill] = level
		}
		e := Employee{ID: source.ID, Name: source.Name, Department: source.Department, Role: source.Role, Grade: grade, GradeLabel: source.Grade, HireDate: source.HireDate, TenureMonths: source.TenureMonths, WorkFormat: source.WorkFormat, PreferredLanguage: source.PreferredLanguage, CareerGoal: source.CareerGoal, LastReviewDate: source.LastReviewDate, Skills: maps.Clone(levels), AssessmentSkills: levels, Version: 1}
		if source.ManagerID != nil {
			e.ManagerID = *source.ManagerID
		}
		if index, exists := existing[e.ID]; exists {
			if !merge {
				return fmt.Errorf("%s.employee_id: duplicate existing ID", prefix)
			}
			current := d.Employees[index]
			current.Skills = maps.Clone(current.AssessmentSkills)
			current.Version = 1
			if !reflect.DeepEqual(current, e) {
				return fmt.Errorf("%s.employee_id: conflicting existing profile; append mode never overwrites assessments", prefix)
			}
			continue
		}
		existing[e.ID] = len(d.Employees)
		d.Employees = append(d.Employees, e)
	}
	for _, e := range d.Employees {
		if e.ManagerID != "" {
			manager, ok := findEmployee(*d, e.ManagerID)
			if !ok || manager.Grade != 4 || manager.Department != e.Department || manager.ID == e.ID {
				return fmt.Errorf("employee %s.manager_id: must reference another Lead in same department", e.ID)
			}
		}
	}
	return nil
}

func decodeOfficialHistory(b []byte, d Dataset) ([]Participation, error) {
	r := csv.NewReader(bytes.NewReader(b))
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("activity_history.csv: %w", err)
	}
	want := []string{"record_id", "employee_id", "event_id", "date", "due_date", "status", "completion_pct", "score", "feedback_rating", "assigned_by"}
	if len(header) != len(want) {
		return nil, fmt.Errorf("activity_history.csv: expected 10 named columns")
	}
	columns := map[string]int{}
	for i, name := range header {
		if !containsString(want, name) {
			return nil, fmt.Errorf("activity_history.csv: unexpected column %s", name)
		}
		if _, ok := columns[name]; ok {
			return nil, fmt.Errorf("activity_history.csv: duplicate column %s", name)
		}
		columns[name] = i
	}
	rows := []Participation{}
	seen := map[string]bool{}
	for line := 2; ; line++ {
		values, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("activity_history.csv row %d: %w", line, err)
		}
		get := func(name string) string { return values[columns[name]] }
		field := func(name string) string { return fmt.Sprintf("activity_history.csv row %d.%s", line, name) }
		p := Participation{RecordID: get("record_id"), EmployeeID: get("employee_id"), ActivityID: get("event_id"), Date: get("date"), DueDate: get("due_date"), Status: get("status"), AssignedBy: get("assigned_by")}
		if p.RecordID == "" || seen[p.RecordID] {
			return nil, fmt.Errorf("%s: empty/duplicate occurrence ID", field("record_id"))
		}
		seen[p.RecordID] = true
		if _, ok := findEmployee(d, p.EmployeeID); !ok {
			return nil, fmt.Errorf("%s: unknown employee", field("employee_id"))
		}
		activity, ok := findActivity(d, p.ActivityID)
		if !ok {
			return nil, fmt.Errorf("%s: unknown event", field("event_id"))
		}
		date, err := exactOfficialDate(p.Date, field("date"))
		if err != nil {
			return nil, err
		}
		if date.After(datasetReferenceTime(d)) {
			return nil, fmt.Errorf("%s: participation after snapshot", field("date"))
		}
		if p.DueDate != "" {
			if _, err := exactOfficialDate(p.DueDate, field("due_date")); err != nil {
				return nil, err
			}
			if !activity.Mandatory {
				return nil, fmt.Errorf("%s: only mandatory events have due dates", field("due_date"))
			}
		}
		pct, err := strconv.Atoi(get("completion_pct"))
		if err != nil || pct < 0 || pct > 100 {
			return nil, fmt.Errorf("%s: expected integer 0–100", field("completion_pct"))
		}
		p.CompletionPct = pct
		switch p.Status {
		case "completed":
			if pct != 100 {
				return nil, fmt.Errorf("%s: completed requires 100", field("completion_pct"))
			}
		case "in_progress":
			if pct > 95 {
				return nil, fmt.Errorf("%s: in_progress requires 0–95", field("completion_pct"))
			}
		case "dropped":
			if pct < 5 || pct > 95 {
				return nil, fmt.Errorf("%s: dropped requires 5–95", field("completion_pct"))
			}
		case "no_show":
			if pct != 0 || activity.Format == "self_paced" {
				return nil, fmt.Errorf("%s: no_show requires scheduled event and zero completion", field("status"))
			}
		case "declined":
			if pct != 0 || p.AssignedBy == "self" {
				return nil, fmt.Errorf("%s: declined requires manager/HR assignment and zero completion", field("status"))
			}
		case "overdue":
			if !activity.Mandatory || p.DueDate == "" || pct > 95 {
				return nil, fmt.Errorf("%s: overdue requires mandatory due date and 0–95 completion", field("status"))
			}
		default:
			return nil, fmt.Errorf("%s: unsupported status %q", field("status"), p.Status)
		}
		if !containsString([]string{"self", "manager", "hr"}, p.AssignedBy) {
			return nil, fmt.Errorf("%s: expected self/manager/hr", field("assigned_by"))
		}
		for _, optional := range []struct {
			name   string
			max    int
			target **int
		}{{"score", 100, &p.Score}, {"feedback_rating", 5, &p.FeedbackRating}} {
			if raw := get(optional.name); raw != "" {
				value, err := strconv.Atoi(raw)
				minimum := 0
				if optional.name == "feedback_rating" {
					minimum = 1
				}
				if err != nil || value < minimum || value > optional.max {
					return nil, fmt.Errorf("%s: value outside supported range", field(optional.name))
				}
				*optional.target = &value
			}
		}
		rows = append(rows, p)
	}
	return rows, nil
}

// Only new source occurrences after their profile's last assessment are applied.
// Existing imported occurrences and current state are never replayed on merge.
func replayOfficialCompletions(d *Dataset, newRows []Participation) int {
	rows := append([]Participation(nil), newRows...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Date != rows[j].Date {
			return rows[i].Date < rows[j].Date
		}
		return rows[i].RecordID < rows[j].RecordID
	})
	indices := map[string]int{}
	for i, e := range d.Employees {
		indices[e.ID] = i
	}
	activities := map[string]Activity{}
	for _, a := range d.Activities {
		activities[a.ID] = a
	}
	historyIndex := map[string]int{}
	for i, p := range d.History {
		historyIndex[p.RecordID] = i
	}
	applied := 0
	for _, p := range rows {
		i := indices[p.EmployeeID]
		e := &d.Employees[i]
		if p.Status != "completed" || p.Date <= e.LastReviewDate {
			continue
		}
		a := activities[p.ActivityID]
		projections, _ := projectActivity(*e, a, contextFor(*d, *e).Gaps)
		changes := []Change{}
		for _, projection := range projections {
			if projection.Effect != nil && projection.AppliedGain > 0 {
				changes = append(changes, *projection.Effect)
				e.Skills[projection.Assessment.Skill] = projection.Effect.After
			}
		}
		if index, ok := historyIndex[p.RecordID]; ok {
			d.History[index].Changes = changes
			d.History[index].Projections = projections
			d.History[index].RuleVersion = OfficialGrowthRuleVersion
		}
		applied++
	}
	return applied
}

func sourceParticipation(p Participation) Participation {
	p.Changes = nil
	p.Projections = nil
	p.RuleVersion = ""
	return p
}

func prepareJuryZip(base Dataset, reader io.ReaderAt, size int64) (Dataset, ImportSummary, error) {
	if base.Source != "halyk-v1" {
		return Dataset{}, ImportSummary{}, fmt.Errorf("import the full official kit before a employee update")
	}
	files, err := officialZipFiles(reader, size)
	if err != nil {
		return Dataset{}, ImportSummary{}, err
	}
	if files["employees.json"] == nil && files["activity_history.csv"] == nil {
		return Dataset{}, ImportSummary{}, fmt.Errorf("employee update ZIP requires employees.json and/or activity_history.csv")
	}
	if files["events.json"] != nil || files["skills.json"] != nil {
		return Dataset{}, ImportSummary{}, fmt.Errorf("employee update accepts employee/history files only; catalogue changes require a reviewed replacement")
	}
	d := cloneDataset(base)
	beforeEmployees, beforeHistory := len(d.Employees), len(d.History)
	if bytes := files["employees.json"]; bytes != nil {
		var batch officialEmployees
		if err = decodeOfficialJSON("employees.json", bytes, &batch); err != nil {
			return Dataset{}, ImportSummary{}, err
		}
		if err = validateOfficialMeta(batch.Meta, base.ReferenceDate); err != nil {
			return Dataset{}, ImportSummary{}, err
		}
		if err = appendOfficialEmployees(&d, batch.Employees, true); err != nil {
			return Dataset{}, ImportSummary{}, err
		}
	}
	newRows := []Participation{}
	if bytes := files["activity_history.csv"]; bytes != nil {
		rows, err := decodeOfficialHistory(bytes, d)
		if err != nil {
			return Dataset{}, ImportSummary{}, err
		}
		existing := map[string]Participation{}
		for _, row := range d.History {
			if row.RecordID != "" {
				existing[row.RecordID] = row
			}
		}
		for _, row := range rows {
			if prior, ok := existing[row.RecordID]; ok {
				if !reflect.DeepEqual(sourceParticipation(prior), sourceParticipation(row)) {
					return Dataset{}, ImportSummary{}, fmt.Errorf("activity_history.csv record_id %s: conflicting existing occurrence", row.RecordID)
				}
				continue
			}
			// Late backfilled gains are order-sensitive when caps differ. Preserve
			// current transaction/audit semantics: require a reviewed replacement
			// instead of silently applying an old gain after later completions.
			if employee, exists := findEmployee(base, row.EmployeeID); exists && row.Status == "completed" && row.Date > employee.LastReviewDate {
				for _, prior := range base.History {
					if prior.EmployeeID != row.EmployeeID || prior.Status != "completed" || prior.Date <= employee.LastReviewDate {
						continue
					}
					if prior.Date > row.Date || (prior.Date == row.Date && prior.RecordID > row.RecordID) {
						return Dataset{}, ImportSummary{}, fmt.Errorf("activity_history.csv record_id %s: out-of-order completion for an existing profile; use reviewed full replacement so capped gains replay chronologically", row.RecordID)
					}
				}
			}
			d.History = append(d.History, row)
			newRows = append(newRows, row)
		}
	}
	applied := replayOfficialCompletions(&d, newRows)
	touched := map[string]bool{}
	for _, p := range newRows {
		touched[p.EmployeeID] = true
	}
	for i := range d.Employees {
		if touched[d.Employees[i].ID] && i < beforeEmployees {
			d.Employees[i].Version++
		}
	}
	d.Recommendations = map[string]RecommendationSet{}
	if err = validateDataset(d); err != nil {
		return Dataset{}, ImportSummary{}, fmt.Errorf("employee update: %w", err)
	}
	return d, officialSummary(d, applied, len(d.Employees)-beforeEmployees, len(d.History)-beforeHistory), nil
}
