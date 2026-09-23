package careerquest

// Provider selection is grounded in local eligibility, growth and history facts.
// Every proposal is validated again before it can be saved.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"
)

const ProviderPromptVersion = "verified-selection-v4"

type ProviderCandidate struct {
	Activity Activity            `json:"activity"`
	Facts    CandidateEvaluation `json:"facts"`
}

type ProviderInput struct {
	EmployeeID        string              `json:"employee_id"`
	EmployeeVersion   int                 `json:"employee_version"`
	DatasetRevision   string              `json:"dataset_revision"`
	ReferenceDate     time.Time           `json:"reference_date"`
	GrowthRuleVersion string              `json:"growth_rule_version"`
	PolicyVersion     string              `json:"policy_version"`
	PromptVersion     string              `json:"prompt_version"`
	Context           EmployeeContext     `json:"context"`
	Requirements      []Requirement       `json:"requirements"`
	Candidates        []ProviderCandidate `json:"candidates"`
}

// Reasons contain references and categories, never unchecked prose or numbers.
// Displayed arithmetic is regenerated from canonical projections after selection.
type ProviderReason struct {
	Category       string `json:"category"`
	SkillID        string `json:"skill_id,omitempty"`
	HistoryIndices []int  `json:"history_indices,omitempty"`
}

type ProviderComparison struct {
	ActivityID string           `json:"activity_id"`
	Reasons    []ProviderReason `json:"reasons"`
}

type ProviderSelection struct {
	ActivityID string              `json:"activity_id"`
	Reasons    []ProviderReason    `json:"reasons"`
	Comparison *ProviderComparison `json:"comparison,omitempty"`
}

type ProviderProposal struct {
	ResponseID      string              `json:"-"`
	EmployeeID      string              `json:"employee_id"`
	EmployeeVersion int                 `json:"employee_version"`
	DatasetRevision string              `json:"dataset_revision"`
	Selections      []ProviderSelection `json:"selections"`
}

type SelectionProvider interface {
	Name() string
	Select(context.Context, ProviderInput) (ProviderProposal, error)
}

func providerInput(d Dataset, e Employee, reference time.Time) ProviderInput {
	// Detach provider-visible maps, so even an in-process provider cannot alter
	// the canonical snapshot used later to validate its output.
	copy := cloneDataset(d)
	e, _ = findEmployee(copy, e.ID)
	e = Employee{ID: e.ID, Role: e.Role, Grade: e.Grade, TenureMonths: e.TenureMonths, Skills: e.Skills, Version: e.Version}
	input := ProviderInput{EmployeeID: e.ID, EmployeeVersion: e.Version, DatasetRevision: reasoningRevision(d), ReferenceDate: referenceDay(reference), GrowthRuleVersion: growthVersionForDataset(d), PolicyVersion: ReasoningPolicyVersion, PromptVersion: ProviderPromptVersion, Context: contextFor(copy, e)}
	for _, r := range copy.Requirements {
		if r.Role == e.Role && r.Grade == e.Grade+1 {
			input.Requirements = append(input.Requirements, r)
		}
	}
	for _, evaluation := range evaluateCandidatesAt(copy, e, reference) {
		a, _ := findActivity(copy, evaluation.ActivityID)
		input.Candidates = append(input.Candidates, ProviderCandidate{Activity: a, Facts: evaluation})
	}
	return input
}

// Provider transports decode through this function. Extra fields (including
// fabricated gains or free-text claims), excessive output and trailing JSON fail.
func decodeProviderProposal(raw []byte) (ProviderProposal, error) {
	var p ProviderProposal
	if len(raw) > 64*1024 {
		return p, fmt.Errorf("provider output exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return p, fmt.Errorf("provider output has trailing content")
	}
	return p, nil
}

func providerReasons(e Employee, a Activity, c CandidateEvaluation, reasons []ProviderReason, minimum int) ([]string, error) {
	if len(reasons) < minimum || len(reasons) > 6 {
		return nil, fmt.Errorf("unsupported rationale count")
	}
	seen := map[string]bool{}
	text := []string{}
	for _, reason := range reasons {
		key := reason.Category
		if reason.Category == "skill_projection" {
			key += ":" + reason.SkillID
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate rationale category")
		}
		seen[key] = true
		if reason.Category != "skill_projection" && reason.SkillID != "" {
			return nil, fmt.Errorf("unexpected skill evidence")
		}
		if reason.Category != "history_exact" && reason.Category != "history_related" && len(reason.HistoryIndices) != 0 {
			return nil, fmt.Errorf("unexpected history evidence")
		}
		switch reason.Category {
		case "grade_fit":
			text = append(text, fmt.Sprintf("Grade fit: %s grade %d is in the activity's eligible audience.", e.Role, e.Grade))
		case "target_gap":
			text = append(text, fmt.Sprintf("Target requirements: this step closes %d assessed gap points, including %d explicitly critical points.", c.Benefits.GapClosure, c.Benefits.CriticalGapClosure))
		case "duration":
			text = append(text, fmt.Sprintf("Time commitment: the catalog lists %g hours.", activityHours(a)))
		case "skill_projection":
			found := false
			for _, p := range c.Projections {
				if p.Assessment.Skill == reason.SkillID && p.Assessment.Assessed && p.HasTarget && p.Effect != nil {
					found = true
					text = append(text, fmt.Sprintf("Skill evidence: %s %d → %d; advertised +%d capped at %d, applies +%d; target %d, remaining gap %d.", reason.SkillID, p.Effect.Before, p.Effect.After, p.Advertised.Gain, p.Advertised.MaxLevel, p.AppliedGain, p.Assessment.Target, *p.RemainingGap))
				}
			}
			if !found {
				return nil, fmt.Errorf("unsupported skill projection reference")
			}
		case "history_exact", "history_related":
			facts := c.History.Exact
			if reason.Category == "history_related" {
				facts = c.History.Related
			}
			if len(reason.HistoryIndices) == 0 {
				return nil, fmt.Errorf("history evidence is required")
			}
			available := map[int]ParticipationEvidence{}
			for _, fact := range facts {
				if fact.Record.HistoryIndex != nil {
					available[*fact.Record.HistoryIndex] = fact
				}
			}
			used := map[int]bool{}
			counts := map[string]int{}
			for _, index := range reason.HistoryIndices {
				fact, ok := available[index]
				if !ok || used[index] || fact.Participation.EmployeeID != e.ID {
					return nil, fmt.Errorf("unsupported or duplicate history reference")
				}
				used[index] = true
				counts[fact.Participation.Status]++
			}
			text = append(text, fmt.Sprintf("%s evidence: %s in the referenced recorded observations. Collection coverage is unknown.", strings.ReplaceAll(reason.Category, "_", " "), outcomeText(counts)))
		default:
			return nil, fmt.Errorf("unknown rationale category")
		}
	}
	return text, nil
}

// Re-evaluate each selected step against the updated virtual skills. The model
// may choose a defensible different order; it need not reproduce rules ranking.
func validateProviderProposal(d Dataset, e Employee, reference time.Time, p ProviderProposal, provider string) (RecommendationSet, error) {
	canonical, ok := findEmployee(d, e.ID)
	if !ok || canonical.Version != e.Version {
		return RecommendationSet{}, fmt.Errorf("employee snapshot is stale")
	}
	e = canonical
	revision := reasoningRevision(d)
	if p.EmployeeID != e.ID || p.EmployeeVersion != e.Version || p.DatasetRevision != revision {
		return RecommendationSet{}, fmt.Errorf("wrong employee or data reference")
	}
	if len(p.Selections) < 1 || len(p.Selections) > 3 {
		return RecommendationSet{}, fmt.Errorf("select one to three activities")
	}
	set := RecommendationSet{Version: e.Version, Source: "provider-validated", Items: []Recommendation{}, Message: "Follow these steps in order. Projected gains include earlier steps; actual skills change only after completion.", Evidence: &RecommendationEvidence{DatasetRevision: revision, EmployeeID: e.ID, ReferenceDate: referenceDay(reference), PolicyVersion: ReasoningPolicyVersion, GrowthRuleVersion: growthVersionForDataset(d), Provider: provider, Validation: RecommendationValidated, Limitations: []string{"Selections are validated against recorded facts. Skill projections are conditional on completing each step; missing assessments remain unknown."}}}
	virtual := e
	virtual.Skills = map[string]int{}
	for skill, level := range e.Skills {
		virtual.Skills[skill] = level
	}
	seen := map[string]bool{}
	prior := []string{}
	for step, selection := range p.Selections {
		if seen[selection.ActivityID] {
			return RecommendationSet{}, fmt.Errorf("duplicate selected activity")
		}
		candidates := evaluateCandidates(d, virtual, reference, revision, step+1, prior)
		available := map[string]CandidateEvaluation{}
		for _, candidate := range candidates {
			if !seen[candidate.ActivityID] {
				available[candidate.ActivityID] = candidate
			}
		}
		candidate, exists := available[selection.ActivityID]
		if !exists {
			return RecommendationSet{}, fmt.Errorf("unknown, ineligible or non-useful activity at step %d", step+1)
		}
		a, _ := findActivity(d, selection.ActivityID)
		a.Growth = maps.Clone(a.Growth)
		a.SkillPrerequisites = maps.Clone(a.SkillPrerequisites)
		a.Roles = append([]string(nil), a.Roles...)
		a.Prerequisites = append([]string(nil), a.Prerequisites...)
		a.TargetGrades = append([]int(nil), a.TargetGrades...)
		a.UpcomingSessions = append([]string(nil), a.UpcomingSessions...)
		reasons, err := providerReasons(virtual, a, candidate, selection.Reasons, 3)
		if err != nil {
			return RecommendationSet{}, err
		}
		comparison := selection.Comparison
		// Earlier gains can unlock alternatives absent from the provider's initial
		// shortlist. Supply a factual comparison locally when none was proposed.
		if comparison == nil {
			for _, alternative := range candidates {
				if alternative.ActivityID != selection.ActivityID && !seen[alternative.ActivityID] {
					comparison = &ProviderComparison{ActivityID: alternative.ActivityID, Reasons: []ProviderReason{{Category: "target_gap"}, {Category: "duration"}}}
					break
				}
			}
		}
		if comparison != nil {
			alternative, valid := available[comparison.ActivityID]
			if !valid || comparison.ActivityID == selection.ActivityID {
				return RecommendationSet{}, fmt.Errorf("unsupported comparison alternative")
			}
			altActivity, _ := findActivity(d, comparison.ActivityID)
			altReasons, err := providerReasons(virtual, altActivity, alternative, comparison.Reasons, 1)
			if err != nil {
				return RecommendationSet{}, err
			}
			reasons = append(reasons, fmt.Sprintf("Compared with %s: selected closes %d gap points in %g hours; alternative closes %d in %g hours. The provider selected the order; these factual values do not imply the rules score was highest.", altActivity.Title, candidate.Benefits.GapClosure, activityHours(a), alternative.Benefits.GapClosure, activityHours(altActivity)))
			reasons = append(reasons, altReasons...)
			candidate.Comparisons = []CandidateComparison{{AlternativeActivityID: alternative.ActivityID, Reasons: altReasons, SelectedBenefits: candidate.Benefits, AlternativeBenefits: alternative.Benefits}}
		}
		r := Recommendation{Activity: a, Closure: candidate.Benefits.GapClosure, Score: candidate.Benefits.Score, Reasons: reasons, Changes: []Change{}}
		for _, projection := range candidate.Projections {
			if projection.Effect != nil && projection.AppliedGain > 0 {
				r.Changes = append(r.Changes, *projection.Effect)
				virtual.Skills[projection.Assessment.Skill] = projection.Effect.After
			}
		}
		candidate.Selected = true
		set.Evidence.Candidates = append(set.Evidence.Candidates, candidate)
		set.Items = append(set.Items, r)
		seen[a.ID] = true
		prior = append(prior, a.ID)
	}
	return set, nil
}

type ProviderRun struct {
	Result                                         RecommendationSet
	State                                          string
	Provider, Model, PromptVersion, FallbackReason string
	Elapsed                                        time.Duration
	Cached                                         bool
	AIValidated                                    bool
}

// Future adapters and publishers must honor this context, including queueing and
// precommit freshness checks. One absolute budget is shared by every stage.
type ProviderPublisher func(context.Context, RecommendationSet) error
type ProviderService struct {
	Budget           time.Duration
	GenerationBudget time.Duration
	slots            chan struct{}
}

func newProviderService() *ProviderService {
	return &ProviderService{Budget: 20 * time.Second, GenerationBudget: 16 * time.Second, slots: make(chan struct{}, 2)}
}

func (s *ProviderService) Execute(parent context.Context, d Dataset, e Employee, reference time.Time, provider SelectionProvider, publish ProviderPublisher) (run ProviderRun, err error) {
	started := time.Now()
	defer func() { run.Elapsed = time.Since(started) }()
	budget := s.Budget
	if budget <= 0 || budget > 60*time.Second {
		budget = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return
	}
	canonical, exists := findEmployee(d, e.ID)
	if !exists {
		return run, fmt.Errorf("employee unavailable")
	}
	e = canonical
	run.PromptVersion = ProviderPromptVersion
	run.Result = recommendAt(d, e, reference)
	run.State, run.Provider = "rules", "local-rules"
	if provider != nil && len(run.Result.Items) > 0 {
		input := providerInput(d, e, reference)
		run.Provider = provider.Name()
		generationBudget := s.GenerationBudget
		if generationBudget <= 0 || generationBudget >= budget {
			generationBudget = budget * 4 / 5
		}
		generationCtx, stop := context.WithTimeout(ctx, generationBudget)
		proposal, providerErr := s.generate(generationCtx, provider, input)
		stop()
		if providerErr == nil {
			var validated RecommendationSet
			validated, providerErr = validateProviderProposal(d, e, reference, proposal, provider.Name())
			if providerErr != nil {
				providerErr = &providerFailure{message: "The AI plan failed a profile check: " + providerErr.Error() + "."}
			}
			if providerErr == nil {
				remote, actualAI := provider.(*OpenAIProvider)
				if !actualAI {
					providerErr = fmt.Errorf("unsupported AI provider")
				} else {
					run.Result = validated
					run.Result.Source = "ai-assisted"
					run.Result.Evidence.Model = remote.model
					run.Result.Evidence.PromptVersion = ProviderPromptVersion
					run.Result.Evidence.ResponseID = proposal.ResponseID
					run.Result.Message = "AI selected these steps using your profile, next-grade requirements and recorded history. Evidence and projected skill changes are checked by the application."
					run.State, run.Model, run.AIValidated = "ai", remote.model, true
				}
			}
		}
		if providerErr != nil {
			run.State = "rules-fallback"
			run.FallbackReason = "The AI response could not be validated against your current profile."
			var failure *providerFailure
			if errors.As(providerErr, &failure) {
				run.FallbackReason = failure.message
			}
			if errors.Is(providerErr, context.DeadlineExceeded) {
				run.FallbackReason = "The AI request timed out."
			}
			run.Result.Message = "Rules fallback: " + run.FallbackReason + " " + run.Result.Message
		}
	} else if provider == nil {
		run.Result.Message = "Rules-based recommendations: AI is not configured. " + run.Result.Message
	}
	if err = ctx.Err(); err != nil {
		run.State = "deadline-exceeded"
		return
	}
	generatedAt := time.Now().UTC()
	run.Result.Evidence.GeneratedAt = &generatedAt
	if publish != nil {
		if err = publish(ctx, run.Result); err != nil {
			run.State = "publish-failed"
			if errors.Is(err, ErrStaleRecommendation) {
				run.State = "stale-input"
				run.Result = RecommendationSet{}
			}
		}
	}
	return
}

func (s *ProviderService) generate(ctx context.Context, provider SelectionProvider, input ProviderInput) (ProviderProposal, error) {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return ProviderProposal{}, ctx.Err()
	}
	type response struct {
		proposal ProviderProposal
		err      error
	}
	result := make(chan response, 1)
	go func() {
		defer func() { <-s.slots }()
		p, err := provider.Select(ctx, input)
		result <- response{p, err}
	}()
	select {
	case got := <-result:
		return got.proposal, got.err
	case <-ctx.Done():
		return ProviderProposal{}, ctx.Err()
	}
}
