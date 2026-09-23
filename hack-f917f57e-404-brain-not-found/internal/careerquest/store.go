package careerquest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	mu   sync.RWMutex
	data Dataset
	path string
}

func openStore(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Start with an empty workspace; HR imports the initial dataset.
		return &Store{data: Dataset{DataEpoch: 1, Recommendations: map[string]RecommendationSet{}}, path: path}, nil
	} else if err != nil {
		return nil, err
	}
	var d Dataset
	if err = json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if err = validateDataset(d); err != nil {
		return nil, err
	}
	if d.Recommendations == nil {
		d.Recommendations = map[string]RecommendationSet{}
	}
	if d.DataEpoch == 0 {
		d.DataEpoch = 1
	}
	return &Store{data: d, path: path}, nil
}

func (s *Store) snapshot() Dataset {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneDataset(s.data)
}

// Epoch binds identities to a particular imported population; revision binds an
// in-flight computation to all relevant profile/history/catalogue input.
type StoreVersion struct {
	Revision string
	Epoch    uint64
}

var ErrStaleRecommendation = errors.New("recommendation input changed; regenerate")
var ErrStaleIdentity = errors.New("dataset changed; sign in again")

func effectiveEpoch(d Dataset) uint64 {
	if d.DataEpoch == 0 {
		return 1
	}
	return d.DataEpoch
}

func (s *Store) dataEpoch() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return effectiveEpoch(s.data)
}

func (s *Store) snapshotVersioned() (Dataset, StoreVersion) {
	d := s.snapshot()
	return d, StoreVersion{Revision: reasoningRevision(d), Epoch: effectiveEpoch(d)}
}

func (s *Store) publishRecommendation(employeeID string, input StoreVersion, set RecommendationSet) error {
	return s.publishRecommendationContext(context.Background(), employeeID, input, set)
}

func (s *Store) publishRecommendationContext(ctx context.Context, employeeID string, input StoreVersion, set RecommendationSet) error {
	return s.update(func(d *Dataset) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, exists := findEmployee(*d, employeeID)
		if !exists || effectiveEpoch(*d) != input.Epoch || reasoningRevision(*d) != input.Revision || set.Version != e.Version {
			return ErrStaleRecommendation
		}
		if !recommendationCurrent(*d, e, set, datasetReferenceTime(*d)) {
			return ErrStaleRecommendation
		}
		d.Recommendations[employeeID] = set
		return nil
	})
}

// A bounded contention fallback only for the fast in-process rules selector.
// Never execute a provider/model/network request while holding this store lock.
// The optimistic path remains primary; this avoids starving a local rules click
// when unrelated completions continuously change global evidence references.
func (s *Store) generateRulesAtEpoch(employeeID string, epoch uint64) error {
	return s.update(func(d *Dataset) error {
		if effectiveEpoch(*d) != epoch {
			return ErrStaleIdentity
		}
		e, exists := findEmployee(*d, employeeID)
		if !exists {
			return ErrStaleIdentity
		}
		set := recommendAt(*d, e, datasetReferenceTime(*d))
		if set.Evidence == nil || set.Evidence.Validation != RecommendationValidated {
			return fmt.Errorf("local rules validation failed")
		}
		d.Recommendations[employeeID] = set
		return nil
	})
}

func (s *Store) replaceDataset(replacement Dataset) error {
	if err := validateDataset(replacement); err != nil {
		return err
	}
	return s.update(func(d *Dataset) error {
		next := cloneDataset(replacement)
		next.DataEpoch = effectiveEpoch(*d) + 1
		next.Recommendations = map[string]RecommendationSet{}
		for i := range next.Employees {
			next.Employees[i].Version = 1
		}
		*d = next
		return nil
	})
}

func (s *Store) commitImportedDataset(replacement Dataset, expected StoreVersion, replace bool) error {
	if err := validateDataset(replacement); err != nil {
		return err
	}
	return s.update(func(d *Dataset) error {
		if effectiveEpoch(*d) != expected.Epoch || reasoningRevision(*d) != expected.Revision {
			return ErrStaleRecommendation
		}
		next := cloneDataset(replacement)
		next.DataEpoch = effectiveEpoch(*d)
		if replace {
			next.DataEpoch++
			for i := range next.Employees {
				next.Employees[i].Version = 1
			}
		}
		next.Recommendations = map[string]RecommendationSet{}
		*d = next
		return nil
	})
}

// Hold one lock and commit one snapshot: participation and skill updates are atomic.
func (s *Store) update(fn func(*Dataset) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneDataset(s.data)
	if err := fn(&next); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(s.path+".tmp", s.path); err != nil {
		return err
	}
	s.data = next
	return nil
}

func (s *Store) complete(employeeID, activityID, requestID string) error {
	return s.completeAtEpoch(employeeID, activityID, requestID, 0)
}

// An epoch of zero is reserved for trusted internal callers. HTTP callers pass
// the authenticated session epoch, checked inside the mutation lock.
func (s *Store) completeAtEpoch(employeeID, activityID, requestID string, epoch uint64) error {
	if len(requestID) < 16 || len(requestID) > 128 {
		return fmt.Errorf("invalid completion request")
	}
	return s.update(func(d *Dataset) error {
		if epoch != 0 && effectiveEpoch(*d) != epoch {
			return ErrStaleIdentity
		}
		for _, p := range d.History {
			if p.EmployeeID == employeeID && p.RequestID == requestID {
				if p.ActivityID != activityID {
					return fmt.Errorf("request already used for another activity")
				}
				return nil
			}
		}
		e, ok := findEmployee(*d, employeeID)
		if !ok {
			return fmt.Errorf("employee not found")
		}
		a, ok := findActivity(*d, activityID)
		if !ok {
			return fmt.Errorf("activity not found")
		}
		if !eligible(*d, e, a) {
			return fmt.Errorf("activity is not currently eligible")
		}
		projections, _ := projectActivity(e, a, contextFor(*d, e).Gaps)
		changes := []Change{}
		for _, projection := range projections {
			if projection.Effect != nil && projection.AppliedGain > 0 {
				changes = append(changes, *projection.Effect)
			}
		}
		for i := range d.Employees {
			if d.Employees[i].ID == employeeID {
				for _, change := range changes {
					d.Employees[i].Skills[change.Skill] = change.After
				}
				d.Employees[i].Version++
			}
		}
		d.History = append(d.History, Participation{EmployeeID: employeeID, ActivityID: activityID, Status: "completed", CompletionPct: 100, AssignedBy: "self", Date: datasetReferenceTime(*d).Format(time.RFC3339), RequestID: requestID, Changes: changes, Projections: projections, RuleVersion: activityRuleVersion(a)})
		delete(d.Recommendations, employeeID)
		return nil
	})
}
