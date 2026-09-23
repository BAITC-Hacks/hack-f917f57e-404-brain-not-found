package careerquest

import "sort"

const OfficialGrowthRuleVersion = "halyk-capped-v1"

// ProfileSkill preserves real assessments even when this target does not use them.
// Skill is the source identifier: the prototype has no separate label metadata.
type ProfileSkill struct {
	Skill                string
	Label                string
	MetadataMissing      bool
	Current, Target, Gap int
	Assessed, HasTarget  bool
}

type ProgressTerm struct {
	Skill                    string
	Current, Target, Counted int
	Assessed                 bool
}

func profileContext(d Dataset, e Employee) EmployeeContext {
	c := EmployeeContext{Employee: e, ProgressState: "Next-grade requirements are missing."}
	labels := map[string]string{}
	for _, skill := range d.SkillCatalog {
		labels[skill.ID] = skill.Name
	}
	profile := map[string]ProfileSkill{}
	for skill, level := range e.Skills {
		profile[skill] = ProfileSkill{Skill: skill, Current: level, Assessed: true}
	}
	for _, target := range d.Requirements {
		if target.Role != e.Role || target.Grade != e.Grade+1 {
			continue
		}
		c.HasTarget = true
		for skill, level := range target.Skills {
			current, assessed := e.Skills[skill]
			gap, counted := 0, 0
			if assessed {
				gap = max(0, level-current)
				counted = min(current, level)
			} else {
				c.Missing++
			}
			c.ProgressNumerator += counted
			c.ProgressDenominator += level
			c.Gaps = append(c.Gaps, SkillGap{skill, current, level, gap, assessed})
			c.ProgressTerms = append(c.ProgressTerms, ProgressTerm{skill, current, level, counted, assessed})
			profile[skill] = ProfileSkill{Skill: skill, Current: current, Target: level, Gap: gap, Assessed: assessed, HasTarget: true}
		}
	}
	for _, skill := range profile {
		skill.Label = labels[skill.Skill]
		skill.MetadataMissing = skill.Label == ""
		c.ProfileSkills = append(c.ProfileSkills, skill)
	}
	sort.Slice(c.ProfileSkills, func(i, j int) bool { return c.ProfileSkills[i].Skill < c.ProfileSkills[j].Skill })
	sort.Slice(c.Gaps, func(i, j int) bool { return c.Gaps[i].Skill < c.Gaps[j].Skill })
	sort.Slice(c.ProgressTerms, func(i, j int) bool { return c.ProgressTerms[i].Skill < c.ProgressTerms[j].Skill })
	if c.HasTarget {
		c.ProgressState = "Coverage is undefined: target requirements contain no positive total."
	}
	if c.ProgressDenominator > 0 {
		c.ProgressDefined = true
		c.Progress = c.ProgressNumerator * 100 / c.ProgressDenominator
		c.ProgressState = "Assessed skill coverage of listed next-grade requirements."
		if c.Missing > 0 {
			c.ProgressState = "Partial coverage: unassessed target skills contribute no known points; their levels are unknown."
		}
	}
	for _, p := range d.History {
		if p.EmployeeID == e.ID {
			c.History = append(c.History, detachParticipation(p))
		}
	}
	return c
}

// projectActivity is the versioned numerical boundary for recommendation facts,
// previews and saved completions. Only grow implements the provisional formula.
// Unknown assessments have no Effect and receive no automatic gain.
func projectActivity(e Employee, a Activity, gaps []SkillGap) ([]SkillProjection, int) {
	projections := []SkillProjection{}
	closure := 0
	ruleVersion := activityRuleVersion(a)
	for skill, rule := range a.Growth {
		current, assessed := e.Skills[skill]
		p := SkillProjection{Assessment: SkillGap{Skill: skill, Current: current, Assessed: assessed}, Advertised: rule, RuleVersion: ruleVersion}
		for _, gap := range gaps {
			if gap.Skill == skill {
				p.Assessment = gap
				p.HasTarget = true
				break
			}
		}
		if assessed {
			after := grow(current, rule)
			p.Effect = &Change{Skill: skill, Before: current, After: after}
			p.AppliedGain = after - current
			if p.HasTarget {
				remaining := max(0, p.Assessment.Target-after)
				p.RemainingGap = &remaining
				closure += min(p.Assessment.Gap, p.AppliedGain)
			}
		}
		projections = append(projections, p)
	}
	sort.Slice(projections, func(i, j int) bool { return projections[i].Assessment.Skill < projections[j].Assessment.Skill })
	return projections, closure
}

func activityRuleVersion(a Activity) string {
	if a.RuleVersion != "" {
		return a.RuleVersion
	}
	return StandardGrowthRuleVersion
}

// Evidence and presentation consumers must not retain writable aliases to the
// persisted audit, including pointer fields added for official history.
func detachParticipation(p Participation) Participation {
	p.Changes = append([]Change(nil), p.Changes...)
	p.Projections = append([]SkillProjection(nil), p.Projections...)
	for i := range p.Projections {
		if value := p.Projections[i].Effect; value != nil {
			copy := *value
			p.Projections[i].Effect = &copy
		}
		if value := p.Projections[i].RemainingGap; value != nil {
			copy := *value
			p.Projections[i].RemainingGap = &copy
		}
	}
	if p.Score != nil {
		copy := *p.Score
		p.Score = &copy
	}
	if p.FeedbackRating != nil {
		copy := *p.FeedbackRating
		p.FeedbackRating = &copy
	}
	return p
}
