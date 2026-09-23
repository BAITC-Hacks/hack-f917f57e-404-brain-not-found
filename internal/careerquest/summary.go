package careerquest

import (
	"sort"
	"time"
)

// Indexed per-request work has no cross-user cache, and keeps the full history
// unchanged for evidence references. A grouped slice is used only for aggregates
// and eligibility, where global positional references are not emitted.
func groupedHistory(d Dataset) map[string][]Participation {
	groups := make(map[string][]Participation, len(d.Employees))
	for _, row := range d.History {
		groups[row.EmployeeID] = append(groups[row.EmployeeID], row)
	}
	return groups
}

// This is the existence predicate used by evaluateCandidates: exact same raw and
// dated eligibility checks, followed by shared growth simulation and positive
// target-gap closure. Scoring/explanations cannot change whether an option exists.
func hasUsefulCandidateAt(d Dataset, e Employee, gaps []SkillGap, reference time.Time) bool {
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
		if _, closure := simulate(e, activity, gaps); closure > 0 {
			return true
		}
	}
	return false
}

func recommendationCurrentRevision(e Employee, set RecommendationSet, reference time.Time, revision string, versions ...string) bool {
	expectedGrowth := StandardGrowthRuleVersion
	if len(versions) > 0 {
		expectedGrowth = versions[0]
	}
	evidence := set.Evidence
	validSource := set.Source == "rules-based"
	if set.Source == "ai-assisted" && evidence != nil {
		validSource = evidence.Provider == "openai" && evidence.Model != "" && evidence.PromptVersion == ProviderPromptVersion
	}
	return validSource && set.Version == e.Version && evidence != nil &&
		evidence.EmployeeID == e.ID && evidence.Validation == RecommendationValidated &&
		evidence.PolicyVersion == ReasoningPolicyVersion && evidence.GrowthRuleVersion == expectedGrowth &&
		evidence.ReferenceDate.Equal(referenceDay(reference)) && evidence.DatasetRevision == revision
}

func hrSummaryAt(d Dataset, reference time.Time) HRSummary {
	s := HRSummary{Employees: len(d.Employees)}
	counts := map[string]*GapCount{}
	histories := groupedHistory(d)
	revision := ""
	if len(d.Recommendations) > 0 {
		revision = reasoningRevision(d)
	}
	for _, e := range d.Employees {
		local := d
		local.History = histories[e.ID]
		c := contextFor(local, e)
		for _, g := range c.Gaps {
			if counts[g.Skill] == nil {
				counts[g.Skill] = &GapCount{Skill: g.Skill}
			}
			row := counts[g.Skill]
			if !g.Assessed {
				row.Missing++
				s.Missing++
			} else if g.Gap > 0 {
				row.Employees++
				row.Points += g.Gap
			}
		}
		state := "Not generated yet"
		cached, ok := d.Recommendations[e.ID]
		switch {
		case !c.HasTarget:
			state = "Missing next-grade requirements"
		case c.Missing == 0 && c.Progress == 100:
			state = "Listed skill requirements met"
		default:
			useful := hasUsefulCandidateAt(local, e, c.Gaps, reference)
			switch {
			case !useful && c.Missing > 0:
				state = "Assessment needed; no useful assessed step"
			case !useful:
				state = "No suitable activity"
			case ok && recommendationCurrentRevision(e, cached, reference, revision, growthVersionForDataset(d)) && len(cached.Items) > 0:
				state = "Recommended steps available"
				s.Suggested++
			}
		}
		s.Coverage = append(s.Coverage, Coverage{e.Name, state})
	}
	for _, row := range counts {
		s.Gaps = append(s.Gaps, *row)
	}
	sort.Slice(s.Gaps, func(i, j int) bool { return s.Gaps[i].Skill < s.Gaps[j].Skill })
	participation := map[string]ParticipationCount{}
	for _, row := range d.History {
		count := participation[row.ActivityID]
		switch row.Status {
		case "completed":
			count.Completed++
		case "registered":
			count.Registered++
		case "no_show":
			count.NoShow++
		case "refused":
			count.Refused++
		case "declined":
			count.Declined++
		case "in_progress":
			count.InProgress++
		case "dropped":
			count.Dropped++
		case "overdue":
			count.Overdue++
		}
		participation[row.ActivityID] = count
	}
	for _, a := range d.Activities {
		row := participation[a.ID]
		row.Title = a.Title
		s.Completions += row.Completed
		s.Participation = append(s.Participation, row)
	}
	return s
}
