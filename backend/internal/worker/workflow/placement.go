package workflow

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// WorkerCandidate is one of the user's computers as placement sees it.
// Capabilities is nil when the computer could not be asked (it is offline, or
// its system info did not arrive), which matches no capability requirement.
type WorkerCandidate struct {
	ID           string
	Name         string
	Online       bool
	Capabilities []string
}

func (c WorkerCandidate) label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

func (c WorkerCandidate) has(capability string) bool {
	return slices.Contains(c.Capabilities, capability)
}

// Placement is the outcome of placing a role. Exactly one of WorkerID and
// Waiting is set: a computer to use now, or why the role must wait.
type Placement struct {
	WorkerID string
	Waiting  string
}

// Place chooses the computer for a role. It never moves a SPECIFIC_WORKER
// role: an offline pinned computer yields a wait, not another computer.
// prefer is the computer the role used last; a capability or any-worker
// placement keeps it while it still qualifies, so a role's agents stay
// together.
//
// An error means the placement can never succeed as stated (the pinned
// computer is not the user's), and the caller blocks rather than waits.
func Place(p *leapmuxv1.WorkerPlacement, candidates []WorkerCandidate, prefer string) (Placement, error) {
	if p == nil {
		p = &leapmuxv1.WorkerPlacement{Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_ANY_WORKER}
	}
	switch p.GetStrategy() {
	case leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER:
		for _, c := range candidates {
			if c.ID != p.GetWorkerId() {
				continue
			}
			if !c.Online {
				return Placement{Waiting: fmt.Sprintf("Waiting for %s to come online", c.label())}, nil
			}
			return Placement{WorkerID: c.ID}, nil
		}
		return Placement{}, fmt.Errorf("computer %s is not registered to this account", p.GetWorkerId())
	case leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_CAPABILITY,
		leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_ANY_WORKER:
	default:
		return Placement{}, fmt.Errorf("unknown placement strategy %v", p.GetStrategy())
	}

	allowed := func(c WorkerCandidate) bool {
		return len(p.GetAllowedWorkerIds()) == 0 || slices.Contains(p.GetAllowedWorkerIds(), c.ID)
	}
	qualifies := func(c WorkerCandidate) bool {
		for _, required := range p.GetRequiredCapabilities() {
			if !c.has(required) {
				return false
			}
		}
		return true
	}
	var eligible []WorkerCandidate
	offlineMatches := 0
	for _, c := range candidates {
		if !allowed(c) {
			continue
		}
		if !c.Online {
			// An offline computer's capabilities are unknown, so it counts as
			// a possible match only for the message.
			offlineMatches++
			continue
		}
		if qualifies(c) {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		need := "an online computer"
		if len(p.GetRequiredCapabilities()) > 0 {
			need = "an online computer with " + strings.Join(p.GetRequiredCapabilities(), ", ")
		}
		if offlineMatches > 0 {
			return Placement{Waiting: fmt.Sprintf("Waiting for %s (%d allowed computer(s) offline)", need, offlineMatches)}, nil
		}
		return Placement{Waiting: "Waiting for " + need}, nil
	}
	for _, c := range eligible {
		if c.ID == prefer {
			return Placement{WorkerID: c.ID}, nil
		}
	}
	slices.SortFunc(eligible, func(a, b WorkerCandidate) int {
		return cmp.Or(cmp.Compare(a.label(), b.label()), cmp.Compare(a.ID, b.ID))
	})
	return Placement{WorkerID: eligible[0].ID}, nil
}

// AllowsWorker reports whether a user's reassignment to workerID respects the
// role's placement. A pinned role may be moved anywhere by the user, because
// the reassignment IS the user's explicit choice; a role restricted to an
// allow-list stays within it, and a capability role keeps its requirements.
func AllowsWorker(p *leapmuxv1.WorkerPlacement, c WorkerCandidate) error {
	if p == nil {
		return nil
	}
	if ids := p.GetAllowedWorkerIds(); len(ids) > 0 && !slices.Contains(ids, c.ID) {
		return fmt.Errorf("%s is not among the computers this role may use", c.label())
	}
	if p.GetStrategy() == leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_CAPABILITY {
		for _, required := range p.GetRequiredCapabilities() {
			if !c.has(required) {
				return fmt.Errorf("%s lacks the capability %q this role requires", c.label(), required)
			}
		}
	}
	return nil
}
