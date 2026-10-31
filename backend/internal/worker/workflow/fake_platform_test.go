package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// fakePlatform stands in for LeapMux beyond the engine's database: a set of
// computers, the agents opened on each, and every message delivered. It
// models what the engine relies on -- a hosting worker remembers which agent
// it opened for which slot, and a resend of one input id delivers once.
type fakePlatform struct {
	mu sync.Mutex

	workers map[string]*fakeWorker
	opened  []openCall
	// delivered holds each input id once, as the hosting worker's dedup does.
	delivered   map[string]delivery
	enqueueErr  error
	openErr     error
	interrupted []string
	tabs        map[string]string // agent id -> workspace
	seq         int
}

type fakeWorker struct {
	name   string
	online bool
	caps   []string
	// agents maps agent id -> gone?
	agents map[string]bool
	// bySlot is what FindAgents answers: the binding table of the worker.
	bySlot map[string]map[string]string // run -> slot -> agent
}

type openCall struct {
	workerID string
	req      *leapmuxv1.OpenAgentRequest
}

type delivery struct {
	workerID, agentID, text string
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{workers: map[string]*fakeWorker{}, delivered: map[string]delivery{}, tabs: map[string]string{}}
}

func (p *fakePlatform) addWorker(id, name string, online bool, caps ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workers[id] = &fakeWorker{name: name, online: online, caps: caps, agents: map[string]bool{}, bySlot: map[string]map[string]string{}}
}

func (p *fakePlatform) setOnline(id string, online bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workers[id].online = online
}

// hostLead records the Lead as the frontend opens it: on the coordinator,
// with the run's binding.
func (p *fakePlatform) hostLead(workerID, agentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workers[workerID].agents[agentID] = false
}

func (p *fakePlatform) closeAgent(workerID, agentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workers[workerID].agents[agentID] = true
}

func (p *fakePlatform) Workers(context.Context) ([]workflow.WorkerCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []workflow.WorkerCandidate
	for id, w := range p.workers {
		c := workflow.WorkerCandidate{ID: id, Name: w.name, Online: w.online}
		if w.online {
			c.Capabilities = w.caps
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b workflow.WorkerCandidate) int {
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	return out, nil
}

func (p *fakePlatform) OpenAgent(_ context.Context, workerID string, req *leapmuxv1.OpenAgentRequest) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.openErr != nil {
		return "", p.openErr
	}
	w := p.workers[workerID]
	if w == nil || !w.online {
		return "", errors.New("unreachable")
	}
	p.seq++
	agentID := fmt.Sprintf("agent-%s-%d", workerID, p.seq)
	w.agents[agentID] = false
	b := req.GetWorkflow()
	if w.bySlot[b.GetRunId()] == nil {
		w.bySlot[b.GetRunId()] = map[string]string{}
	}
	w.bySlot[b.GetRunId()][b.GetSlotId()] = agentID
	p.opened = append(p.opened, openCall{workerID: workerID, req: req})
	return agentID, nil
}

func (p *fakePlatform) AddAgentTab(_ context.Context, workspaceID, _, agentID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tabs[agentID] = workspaceID
	return nil
}

func (p *fakePlatform) EnqueueInput(_ context.Context, workerID, agentID, inputID, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enqueueErr != nil {
		return p.enqueueErr
	}
	w := p.workers[workerID]
	if w == nil || !w.online {
		return errors.New("unreachable")
	}
	if _, dup := p.delivered[inputID]; !dup {
		p.delivered[inputID] = delivery{workerID: workerID, agentID: agentID, text: text}
	}
	return nil
}

func (p *fakePlatform) AgentStates(_ context.Context, workerID string, agentIDs []string) (map[string]workflow.AgentState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w := p.workers[workerID]
	if w == nil || !w.online {
		return nil, errors.New("unreachable")
	}
	out := map[string]workflow.AgentState{}
	for _, id := range agentIDs {
		gone, known := w.agents[id]
		if !known || gone {
			out[id] = workflow.AgentGone
		} else {
			out[id] = workflow.AgentAlive
		}
	}
	return out, nil
}

func (p *fakePlatform) InterruptAgent(_ context.Context, _, agentID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interrupted = append(p.interrupted, agentID)
	return nil
}

func (p *fakePlatform) FindAgents(_ context.Context, workerID, runID string) (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w := p.workers[workerID]
	if w == nil || !w.online {
		return nil, errors.New("unreachable")
	}
	out := map[string]string{}
	for slot, agent := range w.bySlot[runID] {
		out[slot] = agent
	}
	return out, nil
}

// messagesTo lists what one agent received, in no particular order.
func (p *fakePlatform) messagesTo(agentID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, d := range p.delivered {
		if d.agentID == agentID {
			out = append(out, d.text)
		}
	}
	return out
}

func (p *fakePlatform) openCalls() []openCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.opened)
}
