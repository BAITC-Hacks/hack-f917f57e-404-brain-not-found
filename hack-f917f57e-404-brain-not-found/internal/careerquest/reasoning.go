package careerquest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Application policy, not official judging weights. History changes priorities,
// never eligibility. Even a negative-scoring sole useful option may be selected.
const ReasoningPolicyVersion = "skill-related-history-v1"

func referenceDay(reference time.Time) time.Time {
	u := reference.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// The whole input snapshot binds positional history references to their source.
// Cached outputs are excluded to avoid self-invalidating results after a save.
func reasoningRevision(d Dataset) string {
	d.Recommendations = nil
	b, _ := json.Marshal(d)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

func historyDate(raw string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if date, err := time.Parse(layout, raw); err == nil {
			return date.UTC(), true
		}
	}
	return time.Time{}, false
}

func appendNote(notes []string, note string) []string {
	for _, existing := range notes {
		if existing == note {
			return notes
		}
	}
	return append(notes, note)
}

func relationship(candidate, recorded Activity) []string {
	if candidate.ID == recorded.ID {
		return []string{"exact_activity_id"}
	}
	basis := []string{}
	for skill := range candidate.Growth {
		if _, shared := recorded.Growth[skill]; shared {
			basis = append(basis, "develops_skill:"+skill)
		}
	}
	sort.Strings(basis)
	if len(basis) > 0 && candidate.Format != "" && candidate.Format == recorded.Format {
		basis = append(basis, "same_format:"+candidate.Format)
	}
	return basis
}

func historyEvidenceFor(d Dataset, e Employee, activity Activity, reference time.Time, revision string) HistoryEvidence {
	day := referenceDay(reference)
	through := day.AddDate(0, 0, 1).Add(-time.Nanosecond)
	h := HistoryEvidence{
		Exact: []ParticipationEvidence{}, Related: []ParticipationEvidence{},
		ExactOutcomes: map[string]int{}, RelatedOutcomes: map[string]int{},
		Window:      ObservationWindow{Through: &through, ReferenceDate: day},
		MissingData: []string{"History collection coverage is unknown; recorded outcomes do not prove complete attendance history."},
	}
	if start, validStart := historyDate(d.HistoryStart); validStart {
		if end, validEnd := historyDate(d.HistoryEnd); validEnd && !start.After(end) {
			h.Window.From = &start
			h.MissingData = []string{fmt.Sprintf("Documented history collection period: %s through %s; record absence is not an outcome.", start.Format("2006-01-02"), end.Format("2006-01-02"))}
		}
	}
	// No event/date deduplication: two rows without an occurrence identifier may
	// represent separate legitimate participations. Request IDs do identify retries.
	firstRequest := map[string]int{}
	conflicting := map[string]bool{}
	for i, row := range d.History {
		if row.EmployeeID != e.ID || occurrenceID(row) == "" {
			continue
		}
		if first, exists := firstRequest[occurrenceID(row)]; exists {
			if !reflect.DeepEqual(d.History[first], row) {
				conflicting[occurrenceID(row)] = true
			}
		} else {
			firstRequest[occurrenceID(row)] = i
		}
	}
	for i, row := range d.History {
		if row.EmployeeID != e.ID {
			continue
		}
		past, exists := findActivity(d, row.ActivityID)
		if !exists {
			h.MissingData = appendNote(h.MissingData, "Some history references unavailable activities; related outcomes may be incomplete.")
			continue
		}
		basis := relationship(activity, past)
		if len(basis) == 0 {
			continue
		}
		index := i
		row = detachParticipation(row)
		fact := ParticipationEvidence{Record: HistoryRecordReference{DatasetRevision: revision, RecordID: row.RecordID, HistoryIndex: &index}, Participation: row, RelationshipBasis: basis}
		switch {
		case occurrenceID(row) != "" && conflicting[occurrenceID(row)]:
			fact.ExclusionReason = "Conflicting records for the same request ID; outcome is unresolved."
		case occurrenceID(row) != "" && firstRequest[occurrenceID(row)] != i:
			fact.ExclusionReason = "Duplicate occurrence with the same request ID; counted once."
		default:
			date, valid := historyDate(row.Date)
			if !valid {
				fact.ExclusionReason = "Missing or invalid participation date; excluded from dated observations."
			} else if date.After(through) {
				fact.ExclusionReason = "Participation date is after the reference date; excluded from observations."
			}
		}
		if fact.ExclusionReason != "" {
			h.Excluded = append(h.Excluded, fact)
			h.MissingData = appendNote(h.MissingData, fact.ExclusionReason)
			continue
		}
		if occurrenceID(row) == "" {
			h.MissingData = appendNote(h.MissingData, "Rows without occurrence IDs are counted separately; duplicate-import identity cannot be established.")
		}
		date, _ := historyDate(row.Date)
		if h.ObservedFrom == nil || date.Before(*h.ObservedFrom) {
			value := date
			h.ObservedFrom = &value
		}
		if h.ObservedThrough == nil || date.After(*h.ObservedThrough) {
			value := date
			h.ObservedThrough = &value
		}
		if row.ActivityID == activity.ID {
			h.Exact = append(h.Exact, fact)
			h.ExactOutcomes[row.Status]++
		} else {
			h.Related = append(h.Related, fact)
			h.RelatedOutcomes[row.Status]++
		}
		switch row.Status {
		case "completed", "registered", "no_show", "refused", "in_progress", "dropped", "declined", "overdue":
		default:
			h.MissingData = appendNote(h.MissingData, "Unrecognized outcomes are retained as recorded and do not receive a no-show/refusal penalty.")
		}
	}
	return h
}

func criticalTargets(d Dataset, e Employee) map[string]bool {
	critical := map[string]bool{}
	for _, target := range d.Requirements {
		if target.Role == e.Role && target.Grade == e.Grade+1 {
			for _, skill := range target.CriticalSkills {
				if _, defined := target.Skills[skill]; defined {
					critical[skill] = true
				}
			}
		}
	}
	return critical
}

// Candidate evidence and saved completions use the same versioned projection.
func candidateProjections(e Employee, a Activity, gaps []SkillGap) ([]SkillProjection, int) {
	return projectActivity(e, a, gaps)
}

// Reusable factual candidate boundary for recommendation providers. The caller supplies the clock;
// no global history date or implied latest-record-as-today assumption is used.
func evaluateCandidatesAt(d Dataset, e Employee, reference time.Time) []CandidateEvaluation {
	return evaluateCandidates(d, e, reference, reasoningRevision(d), 1, nil)
}

func evaluateCandidates(d Dataset, e Employee, reference time.Time, revision string, step int, prior []string) []CandidateEvaluation {
	context := contextFor(d, e)
	critical := criticalTargets(d, e)
	items := []CandidateEvaluation{}
	// A prerequisite must be established by a dated completion by the reference
	// day. Keep the existing eligibility checks as well, so an undated completion
	// never makes a nonrepeatable activity newly recommendable.
	dated := d
	dated.History = nil
	through := referenceDay(reference).AddDate(0, 0, 1).Add(-time.Nanosecond)
	for _, row := range d.History {
		if date, valid := historyDate(row.Date); valid && !date.After(through) {
			dated.History = append(dated.History, row)
		}
	}
	for _, activity := range d.Activities {
		if activity.Mandatory || !eligible(d, e, activity) || !eligible(dated, e, activity) {
			continue
		}
		projections, closure := candidateProjections(e, activity, context.Gaps)
		if closure == 0 {
			continue
		}
		history := historyEvidenceFor(d, e, activity, reference, revision)
		benefits := CandidateBenefits{GapClosure: closure, DurationPenalty: min(activity.Hours, 20)}
		for _, p := range projections {
			if critical[p.Assessment.Skill] && p.Assessment.Assessed && p.HasTarget {
				benefits.CriticalGapClosure += min(p.Assessment.Gap, p.AppliedGain)
			}
		}
		negative := history.ExactOutcomes["no_show"] + history.ExactOutcomes["refused"] + history.ExactOutcomes["declined"] + history.RelatedOutcomes["no_show"] + history.RelatedOutcomes["refused"] + history.RelatedOutcomes["declined"]
		benefits.HistoryPenalty = min(30*negative, 120)
		benefits.Score = 100*benefits.GapClosure + 40*benefits.CriticalGapClosure - benefits.DurationPenalty - benefits.HistoryPenalty
		limits := append([]string{}, history.MissingData...)
		if len(critical) == 0 {
			limits = append(limits, "No explicit critical-skill metadata is available; all listed target gaps receive the same base weight.")
		}
		if context.Missing > 0 {
			limits = append(limits, "Some target skills are unassessed; their gains and gap closure are not assumed.")
		}
		if d.Source != "halyk-v1" {
			limits = append(limits, "Growth uses the standard capped rule; official dataset rules apply only to official imports.")
		}
		items = append(items, CandidateEvaluation{
			ActivityID: activity.ID, EmployeeID: e.ID, Step: step, PriorActivityIDs: append([]string{}, prior...),
			Eligible: true, EligibilityReasons: []string{"Role and grade match the activity audience.", "Required activities are already completed; repeatability permits participation."},
			Projections: projections, History: history, Limitations: limits, Benefits: benefits,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Benefits.Score == items[j].Benefits.Score {
			return items[i].ActivityID < items[j].ActivityID
		}
		return items[i].Benefits.Score > items[j].Benefits.Score
	})
	return items
}

func outcomeText(outcomes map[string]int) string {
	keys := make([]string, 0, len(outcomes))
	for key := range outcomes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, key := range keys {
		label := key
		switch key {
		case "no_show":
			label = "no-show(s)"
		case "refused":
			label = "refusal(s)"
		case "declined":
			label = "declined invitation(s)"
		case "completed":
			label = "completion(s)"
		case "registered":
			label = "registration(s)"
		case "":
			label = "unknown/empty outcome(s)"
		}
		parts = append(parts, fmt.Sprintf("%d %s", outcomes[key], label))
	}
	return strings.Join(parts, ", ")
}

func evidenceSkills(history HistoryEvidence) []string {
	seen := map[string]bool{}
	for _, fact := range history.Related {
		for _, basis := range fact.RelationshipBasis {
			if strings.HasPrefix(basis, "develops_skill:") {
				seen[strings.TrimPrefix(basis, "develops_skill:")] = true
			}
		}
	}
	result := []string{}
	for skill := range seen {
		result = append(result, skill)
	}
	sort.Strings(result)
	return result
}

func recommendationFor(d Dataset, e Employee, evaluation CandidateEvaluation) Recommendation {
	a, _ := findActivity(d, evaluation.ActivityID)
	a.Growth = maps.Clone(a.Growth)
	a.Roles = append([]string(nil), a.Roles...)
	a.Prerequisites = append([]string(nil), a.Prerequisites...)
	a.SkillPrerequisites = maps.Clone(a.SkillPrerequisites)
	a.TargetGrades = append([]int(nil), a.TargetGrades...)
	a.UpcomingSessions = append([]string(nil), a.UpcomingSessions...)
	r := Recommendation{Activity: a, Changes: []Change{}, Closure: evaluation.Benefits.GapClosure, Score: evaluation.Benefits.Score}
	r.Reasons = []string{
		fmt.Sprintf("Grade fit: your %s grade %d is within this activity's audience (grades %d–%d).", e.Role, e.Grade, a.MinGrade, a.MaxGrade),
		fmt.Sprintf("Next-grade requirements: this step closes %d assessed gap point(s) toward %s grade %d, including %d point(s) in explicitly critical skills. Meeting skill targets does not automatically grant promotion.", r.Closure, e.Role, e.Grade+1, evaluation.Benefits.CriticalGapClosure),
	}
	for _, p := range evaluation.Projections {
		if p.Effect != nil && p.AppliedGain > 0 {
			r.Changes = append(r.Changes, *p.Effect)
		}
		if p.HasTarget && p.Assessment.Assessed {
			r.Reasons = append(r.Reasons, fmt.Sprintf("Skill evidence: %s is %d against target %d; advertised gain %d up to level %d applies +%d, predicting %d → %d and %d remaining gap point(s).", p.Assessment.Skill, p.Assessment.Current, p.Assessment.Target, p.Advertised.Gain, p.Advertised.MaxLevel, p.AppliedGain, p.Effect.Before, p.Effect.After, *p.RemainingGap))
		}
	}
	h := evaluation.History
	if len(h.Exact) == 0 {
		r.Reasons = append(r.Reasons, "Exact history: no recorded participation in this activity within the dated observations.")
	} else {
		r.Reasons = append(r.Reasons, "Exact history: "+outcomeText(h.ExactOutcomes)+" in this activity.")
	}
	if len(h.Related) == 0 {
		r.Reasons = append(r.Reasons, "Related history: no recorded dated outcomes for other activities sharing developed skills; this is not proof of no previous participation.")
	} else {
		r.Reasons = append(r.Reasons, fmt.Sprintf("Related history: %s across other activities developing %s. Exact and related observations are separate; a record sharing multiple skills is counted once.", outcomeText(h.RelatedOutcomes), strings.Join(evidenceSkills(h), ", ")))
	}
	coverage := "collection coverage is unknown"
	if h.Window.From != nil {
		coverage = "documented collection " + d.HistoryStart + " through " + d.HistoryEnd
	}
	r.Reasons = append(r.Reasons, fmt.Sprintf("History scope: loaded dated observations through %s (UTC); %s. %d record(s) excluded from counts. No-shows and refusals affect ordering but do not block an activity.", h.Window.ReferenceDate.Format("2006-01-02"), coverage, len(h.Excluded)))
	if len(criticalTargets(d, e)) == 0 {
		r.Reasons = append(r.Reasons, "Critical priorities are not specified in your target requirements; all listed gaps receive the same base weight.")
	}
	if contextFor(d, e).Missing > 0 {
		r.Reasons = append(r.Reasons, "Some target skills still need assessment; their gains are not assumed in these predictions.")
	}
	for _, comparison := range evaluation.Comparisons {
		r.Reasons = append(r.Reasons, comparison.Reasons...)
	}
	if len(evaluation.Comparisons) == 0 {
		r.Reasons = append(r.Reasons, "No other unselected eligible activity closes an assessed target gap at this step; no alternative is invented.")
	}
	return r
}

func comparisonFor(selected, alternative CandidateEvaluation, d Dataset) CandidateComparison {
	a, _ := findActivity(d, alternative.ActivityID)
	s, b := selected.Benefits, alternative.Benefits
	why := "The higher score sets the order under the documented rules policy."
	if s.Score == b.Score {
		why = "Scores are tied; activity ID is the deterministic tie-breaker, not a claim of greater benefit."
	}
	reasons := []string{fmt.Sprintf("Why before %s: this step closes %d target-gap point(s) (%d critical), with history penalty %d and duration penalty %d, score %d. That eligible alternative closes %d (%d critical), with history penalty %d and duration penalty %d, score %d. %s", a.Title, s.GapClosure, s.CriticalGapClosure, s.HistoryPenalty, s.DurationPenalty, s.Score, b.GapClosure, b.CriticalGapClosure, b.HistoryPenalty, b.DurationPenalty, b.Score, why)}
	exact, related := outcomeText(alternative.History.ExactOutcomes), outcomeText(alternative.History.RelatedOutcomes)
	if exact == "" {
		exact = "none recorded"
	}
	if related == "" {
		related = "none recorded"
	}
	reasons = append(reasons, fmt.Sprintf("Alternative history for %s: exact outcomes: %s; related outcomes: %s. These are recorded observations, not judgments about motivation.", a.Title, exact, related))
	refs := []HistoryRecordReference{}
	seen := map[int]bool{}
	for _, history := range []HistoryEvidence{selected.History, alternative.History} {
		for _, facts := range [][]ParticipationEvidence{history.Exact, history.Related} {
			for _, fact := range facts {
				if index := fact.Record.HistoryIndex; index != nil && !seen[*index] {
					seen[*index] = true
					refs = append(refs, fact.Record)
				}
			}
		}
	}
	return CandidateComparison{AlternativeActivityID: alternative.ActivityID, Reasons: reasons, HistoryRecords: refs, SelectedBenefits: s, AlternativeBenefits: b}
}

func buildRulesRecommendation(d Dataset, e Employee, reference time.Time) RecommendationSet {
	day := referenceDay(reference)
	set := RecommendationSet{Version: e.Version, Source: "rules-based", Items: []Recommendation{}, Evidence: &RecommendationEvidence{
		DatasetRevision: reasoningRevision(d), EmployeeID: e.ID, ReferenceDate: day,
		PolicyVersion: ReasoningPolicyVersion, GrowthRuleVersion: growthVersionForDataset(d),
		Validation: RecommendationNotValidated, Provider: "local-rules", Candidates: []CandidateEvaluation{},
		Limitations: []string{"Rules-based recommendations; no AI provider was invoked.", "Official schema and growth semantics are not verified.", "History coverage is unknown; no timing or motivation inference is made."},
	}}
	if d.Source == "halyk-v1" {
		set.Evidence.Limitations = []string{"Rules-based recommendations; no AI provider was invoked.", "Official starter-kit capped-growth semantics apply; recorded outcomes do not imply motivation or attendance rates."}
	}
	context := contextFor(d, e)
	if !context.HasTarget {
		set.Message = "Next-grade requirements are missing. Ask HR to import them."
		return set
	}
	virtual := e
	virtual.Skills = map[string]int{}
	for skill, level := range e.Skills {
		virtual.Skills[skill] = level
	}
	selected := map[string]bool{}
	prior := []string{}
	for step := 1; step <= 3; step++ {
		all := evaluateCandidates(d, virtual, day, set.Evidence.DatasetRevision, step, prior)
		options := []CandidateEvaluation{}
		for _, candidate := range all {
			if !selected[candidate.ActivityID] {
				options = append(options, candidate)
			}
		}
		if len(options) == 0 {
			break
		}
		options[0].Selected = true
		if len(options) > 1 {
			options[0].Comparisons = []CandidateComparison{comparisonFor(options[0], options[1], d)}
		}
		pick := recommendationFor(d, virtual, options[0])
		set.Items = append(set.Items, pick)
		set.Evidence.Candidates = append(set.Evidence.Candidates, options...)
		selected[pick.Activity.ID] = true
		prior = append(prior, pick.Activity.ID)
		for _, change := range pick.Changes {
			virtual.Skills[change.Skill] = change.After
		}
	}
	if len(set.Items) == 0 {
		switch {
		case context.Missing > 0:
			set.Message = "No useful step for assessed gaps. Some target skills still need assessment."
		case context.Progress == 100:
			set.Message = "All listed next-grade skill requirements are met. Promotion remains a separate HR decision."
		default:
			set.Message = "No eligible activity currently improves your assessed target gaps."
		}
	} else {
		set.Message = "Follow these steps in order. Later predictions include earlier steps. The rules compare actual gap closure, explicit critical requirements, related participation and duration."
	}
	return set
}

// Validate against the deterministic factual pipeline before declaring rules
// output validated. Model providers must validate selections separately with the same
// candidate facts; a model is not required to reproduce the rules ordering.
func validateRulesRecommendation(d Dataset, e Employee, set RecommendationSet, reference time.Time) error {
	if set.Evidence == nil {
		return fmt.Errorf("recommendation evidence is missing")
	}
	canonical, exists := findEmployee(d, e.ID)
	if !exists || !reflect.DeepEqual(canonical, e) {
		return fmt.Errorf("employee does not match the source snapshot")
	}
	expected := buildRulesRecommendation(d, e, reference)
	copy := set
	evidence := *set.Evidence
	copy.Evidence = &evidence
	copy.Evidence.Validation = RecommendationNotValidated
	if !reflect.DeepEqual(copy, expected) {
		return fmt.Errorf("recommendation or evidence does not match the source snapshot and policy")
	}
	return nil
}

func recommendAt(d Dataset, e Employee, reference time.Time) RecommendationSet {
	set := buildRulesRecommendation(d, e, reference)
	if err := validateRulesRecommendation(d, e, set, reference); err != nil {
		set.Items = []Recommendation{}
		set.Evidence.Validation = RecommendationRejected
		set.Message = "Recommendations could not be validated against the current profile. Reload and try again."
		return set
	}
	set.Evidence.Validation = RecommendationValidated
	return set
}

func recommendationCurrent(d Dataset, e Employee, set RecommendationSet, reference time.Time) bool {
	return recommendationCurrentRevision(e, set, reference, reasoningRevision(d), growthVersionForDataset(d))
}
