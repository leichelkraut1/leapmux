// Package workflow runs multi-agent workflows: reusable teams of agents that a
// Lead agent coordinates across the user's computers.
//
// # Where the coordinator runs
//
// The coordinator of a run is the worker that hosts its Lead agent. It owns
// the run's durable state in its own SQLite database, so a run survives a
// closed browser, a hub restart and its own restart, and the objective, task
// text and results never leave the E2EE boundary: the Hub relays ciphertext
// and stores nothing about a run. Agents on other computers are opened and
// messaged through the same cross-worker channel `leapmux control` uses, and
// they call back into the coordinator over it.
//
// The Lead plans and delegates; the engine owns everything a conversation
// cannot be trusted with: task state, dependencies, placement on computers,
// approvals, retries, and recovery.
package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	defaultMaxAttempts = 3
	defaultMaxAgents   = 8
	maxRoles           = 16
	maxSteps           = 64
	maxNameBytes       = 120
	maxTextBytes       = 64 << 10
	// maxObjectiveBytes bounds what the Lead receives in one message.
	maxObjectiveBytes = 32 << 10
)

// The report outputs a role can require. Anything else in required_outputs
// is a definition error rather than a requirement nothing could satisfy.
const (
	OutputSummary   = "summary"
	OutputCommit    = "commit"
	OutputTests     = "tests"
	OutputArtifacts = "artifacts"
)

var knownOutputs = []string{OutputSummary, OutputCommit, OutputTests, OutputArtifacts}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// ErrInvalid marks a definition or request the caller must correct.
var ErrInvalid = errors.New("invalid workflow definition")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func checkName(what, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalidf("%s needs a name", what)
	}
	if len(name) > maxNameBytes {
		return "", invalidf("%s name is longer than %d bytes", what, maxNameBytes)
	}
	return name, nil
}

func checkText(what, text string) error {
	if len(text) > maxTextBytes {
		return invalidf("%s is longer than %d bytes", what, maxTextBytes)
	}
	return nil
}

// NormalizePlacement validates a placement. A nil placement is valid only
// where the caller inherits one.
func NormalizePlacement(p *leapmuxv1.WorkerPlacement) error {
	if p == nil {
		return nil
	}
	p.WorkerId = strings.TrimSpace(p.GetWorkerId())
	p.RequiredCapabilities = normalizeList(p.GetRequiredCapabilities(), true)
	p.AllowedWorkerIds = normalizeList(p.GetAllowedWorkerIds(), false)
	switch p.GetStrategy() {
	case leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER:
		if p.GetWorkerId() == "" {
			return invalidf("a specific-worker placement names its computer")
		}
		// A pinned role runs on that computer alone; the lists below could
		// only suggest a fallback that must never happen.
		p.RequiredCapabilities = nil
		p.AllowedWorkerIds = nil
	case leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_CAPABILITY:
		if len(p.GetRequiredCapabilities()) == 0 {
			return invalidf("a capability placement lists at least one capability")
		}
		p.WorkerId = ""
	case leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_ANY_WORKER:
		p.WorkerId = ""
		p.RequiredCapabilities = nil
	default:
		return invalidf("placement strategy %v is not one of specific, capability or any", p.GetStrategy())
	}
	return nil
}

func normalizeList(in []string, lower bool) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if lower {
			v = strings.ToLower(v)
		}
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeOptions(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" && v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeProfile validates a saved profile in place.
func NormalizeProfile(p *leapmuxv1.AgentProfile) error {
	if p == nil {
		return invalidf("missing profile")
	}
	name, err := checkName("a profile", p.GetName())
	if err != nil {
		return err
	}
	p.Name = name
	if p.GetAgentProvider() == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return invalidf("profile %q has no provider", name)
	}
	if _, ok := leapmuxv1.AgentProvider_name[int32(p.GetAgentProvider())]; !ok {
		return invalidf("profile %q names an unknown provider", name)
	}
	if err := checkText("profile instructions", p.GetInstructions()); err != nil {
		return err
	}
	p.Description = strings.TrimSpace(p.GetDescription())
	p.WorkingDir = strings.TrimSpace(p.GetWorkingDir())
	p.Options = normalizeOptions(p.GetOptions())
	if p.GetPlacement() == nil {
		p.Placement = &leapmuxv1.WorkerPlacement{Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_ANY_WORKER}
	}
	if err := NormalizePlacement(p.GetPlacement()); err != nil {
		return fmt.Errorf("profile %q: %w", name, err)
	}
	if p.GetMaxInstances() == 0 {
		p.MaxInstances = 1
	}
	return nil
}

// NormalizeTemplate validates a template in place and fills its defaults. It
// checks the role table, that exactly one role leads, that every step names a
// role and only earlier-defined or later steps that exist, and that the step
// graph is acyclic.
func NormalizeTemplate(t *leapmuxv1.WorkflowTemplate) error {
	if t == nil {
		return invalidf("missing template")
	}
	name, err := checkName("a template", t.GetName())
	if err != nil {
		return err
	}
	t.Name = name
	t.Description = strings.TrimSpace(t.GetDescription())
	for what, text := range map[string]string{
		"template instructions": t.GetInstructions(),
		"completion criteria":   t.GetCompletionCriteria(),
	} {
		if err := checkText(what, text); err != nil {
			return err
		}
	}
	if len(t.GetRoles()) == 0 {
		return invalidf("template %q has no roles", name)
	}
	if len(t.GetRoles()) > maxRoles {
		return invalidf("template %q has more than %d roles", name, maxRoles)
	}
	if len(t.GetSteps()) > maxSteps {
		return invalidf("template %q has more than %d steps", name, maxSteps)
	}
	if t.GetDefaultMaxAttempts() == 0 {
		t.DefaultMaxAttempts = defaultMaxAttempts
	}
	if t.GetMaxParallelTasks() == 0 {
		t.MaxParallelTasks = 1
	}
	if t.GetMaxAgents() == 0 {
		t.MaxAgents = defaultMaxAgents
	}
	t.SensitiveOperations = normalizeList(t.GetSensitiveOperations(), false)

	roles := map[string]*leapmuxv1.WorkflowRole{}
	leads := 0
	for _, r := range t.GetRoles() {
		if r == nil {
			return invalidf("template %q has an empty role", name)
		}
		r.Key = strings.TrimSpace(r.GetKey())
		if !keyPattern.MatchString(r.GetKey()) {
			return invalidf("role key %q must be lowercase letters, digits and dashes, starting with a letter", r.GetKey())
		}
		if roles[r.GetKey()] != nil {
			return invalidf("role key %q appears twice", r.GetKey())
		}
		roles[r.GetKey()] = r
		if r.GetName() == "" {
			r.Name = r.GetKey()
		}
		if r.GetIsLead() {
			leads++
		}
		if r.GetGitIsolation() == leapmuxv1.WorkflowGitIsolation_WORKFLOW_GIT_ISOLATION_UNSPECIFIED {
			r.GitIsolation = leapmuxv1.WorkflowGitIsolation_WORKFLOW_GIT_ISOLATION_SHARED
		}
		r.RequiredOutputs = normalizeList(r.GetRequiredOutputs(), true)
		for _, o := range r.GetRequiredOutputs() {
			if !slices.Contains(knownOutputs, o) {
				return invalidf("role %q requires unknown output %q (known: %s)", r.GetKey(), o, strings.Join(knownOutputs, ", "))
			}
		}
		if r.GetAgent() == nil {
			r.Agent = &leapmuxv1.AgentProfile{}
		}
		if r.GetProfileId() == "" && r.GetAgent().GetAgentProvider() == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			return invalidf("role %q needs a profile or a provider", r.GetKey())
		}
		r.Agent.Options = normalizeOptions(r.GetAgent().GetOptions())
		if err := NormalizePlacement(r.GetAgent().GetPlacement()); err != nil {
			return fmt.Errorf("role %q: %w", r.GetKey(), err)
		}
		if err := checkText("role instructions", r.GetAgent().GetInstructions()); err != nil {
			return err
		}
	}
	if leads != 1 {
		return invalidf("template %q needs exactly one Lead role, has %d", name, leads)
	}

	steps := map[string]*leapmuxv1.WorkflowStep{}
	for _, s := range t.GetSteps() {
		if s == nil {
			return invalidf("template %q has an empty step", name)
		}
		s.Key = strings.TrimSpace(s.GetKey())
		if !keyPattern.MatchString(s.GetKey()) {
			return invalidf("step key %q must be lowercase letters, digits and dashes, starting with a letter", s.GetKey())
		}
		if steps[s.GetKey()] != nil {
			return invalidf("step key %q appears twice", s.GetKey())
		}
		steps[s.GetKey()] = s
		title, err := checkName("step "+s.GetKey(), s.GetTitle())
		if err != nil {
			return err
		}
		s.Title = title
		role := roles[s.GetRoleKey()]
		if role == nil {
			return invalidf("step %q names unknown role %q", s.GetKey(), s.GetRoleKey())
		}
		if role.GetIsLead() {
			return invalidf("step %q is assigned to the Lead, which coordinates rather than takes tasks", s.GetKey())
		}
		if err := checkText("step instructions", s.GetInstructions()); err != nil {
			return err
		}
		if s.GetApproval() == leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_UNSPECIFIED {
			s.Approval = leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_NONE
		}
		s.DependsOn = normalizeList(s.GetDependsOn(), false)
	}
	for _, s := range t.GetSteps() {
		for _, dep := range s.GetDependsOn() {
			if steps[dep] == nil {
				return invalidf("step %q depends on unknown step %q", s.GetKey(), dep)
			}
		}
	}
	if cycle := findCycle(t.GetSteps()); cycle != "" {
		return invalidf("steps depend on each other in a cycle through %q", cycle)
	}
	return nil
}

// findCycle returns a step key on a dependency cycle, or "".
func findCycle(steps []*leapmuxv1.WorkflowStep) string {
	deps := map[string][]string{}
	for _, s := range steps {
		deps[s.GetKey()] = s.GetDependsOn()
	}
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var visit func(string) string
	visit = func(k string) string {
		switch state[k] {
		case visiting:
			return k
		case done:
			return ""
		}
		state[k] = visiting
		for _, d := range deps[k] {
			if c := visit(d); c != "" {
				return c
			}
		}
		state[k] = done
		return ""
	}
	for _, s := range steps {
		if c := visit(s.GetKey()); c != "" {
			return c
		}
	}
	return ""
}

// mergeAgent overlays the fields over sets on a copy of base.
func mergeAgent(base, over *leapmuxv1.AgentProfile) *leapmuxv1.AgentProfile {
	out := &leapmuxv1.AgentProfile{}
	if base != nil {
		out = proto.Clone(base).(*leapmuxv1.AgentProfile)
	}
	if over == nil {
		return out
	}
	if over.GetAgentProvider() != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		// Another provider's option ids mean nothing to this one.
		if over.GetAgentProvider() != out.GetAgentProvider() {
			out.Options = nil
		}
		out.AgentProvider = over.GetAgentProvider()
	}
	if len(over.GetOptions()) > 0 {
		if out.Options == nil {
			out.Options = map[string]string{}
		}
		for k, v := range over.GetOptions() {
			out.Options[k] = v
		}
	}
	if over.GetPlacement() != nil {
		out.Placement = proto.Clone(over.GetPlacement()).(*leapmuxv1.WorkerPlacement)
	}
	if over.GetWorkingDir() != "" {
		out.WorkingDir = over.GetWorkingDir()
	}
	if over.GetInstructions() != "" {
		if out.GetInstructions() != "" {
			out.Instructions = out.GetInstructions() + "\n\n" + over.GetInstructions()
		} else {
			out.Instructions = over.GetInstructions()
		}
	}
	if over.GetMaxInstances() != 0 {
		out.MaxInstances = over.GetMaxInstances()
	}
	return out
}

// ResolveRoles produces each role's effective configuration for a run: its
// profile, then the template's role override, then the launch override. The
// result is what the run stores, so a later profile edit never reaches it.
func ResolveRoles(t *leapmuxv1.WorkflowTemplate, profiles []*leapmuxv1.AgentProfile, overrides []*leapmuxv1.WorkflowRoleOverride) ([]*leapmuxv1.WorkflowRole, error) {
	byID := map[string]*leapmuxv1.AgentProfile{}
	for _, p := range profiles {
		if p.GetId() != "" {
			byID[p.GetId()] = p
		}
	}
	over := map[string]*leapmuxv1.WorkflowRoleOverride{}
	for _, o := range overrides {
		if o == nil {
			continue
		}
		over[o.GetRoleKey()] = o
	}
	out := make([]*leapmuxv1.WorkflowRole, 0, len(t.GetRoles()))
	for _, r := range t.GetRoles() {
		role := proto.Clone(r).(*leapmuxv1.WorkflowRole)
		var base *leapmuxv1.AgentProfile
		if r.GetProfileId() != "" {
			base = byID[r.GetProfileId()]
			if base == nil {
				return nil, invalidf("role %q references profile %q, which was not supplied", r.GetKey(), r.GetProfileId())
			}
		}
		agent := mergeAgent(base, r.GetAgent())
		if o := over[r.GetKey()]; o != nil {
			agent = mergeAgent(agent, &leapmuxv1.AgentProfile{
				AgentProvider: o.GetAgentProvider(),
				Options:       o.GetOptions(),
				Placement:     o.GetPlacement(),
				WorkingDir:    strings.TrimSpace(o.GetWorkingDir()),
			})
			delete(over, r.GetKey())
		}
		if agent.GetPlacement() == nil {
			agent.Placement = &leapmuxv1.WorkerPlacement{Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_ANY_WORKER}
		}
		if err := NormalizePlacement(agent.GetPlacement()); err != nil {
			return nil, fmt.Errorf("role %q: %w", r.GetKey(), err)
		}
		if agent.GetAgentProvider() == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			return nil, invalidf("role %q resolves to no provider", r.GetKey())
		}
		agent.Options = normalizeOptions(agent.GetOptions())
		if agent.GetMaxInstances() == 0 {
			agent.MaxInstances = 1
		}
		// The resolved agent describes this role only; the profile's own
		// identity would read as a reference the run does not follow.
		agent.Id, agent.Name, agent.Description, agent.UpdatedAtMs = "", "", "", 0
		role.Agent = agent
		out = append(out, role)
	}
	for key := range over {
		return nil, invalidf("an override names unknown role %q", key)
	}
	return out, nil
}

// leadRole returns the template's Lead role.
func leadRole(roles []*leapmuxv1.WorkflowRole) *leapmuxv1.WorkflowRole {
	for _, r := range roles {
		if r.GetIsLead() {
			return r
		}
	}
	return nil
}

func taskMaxAttempts(t *leapmuxv1.WorkflowTemplate, requested uint32) uint32 {
	if requested > 0 {
		return requested
	}
	if t.GetDefaultMaxAttempts() > 0 {
		return t.GetDefaultMaxAttempts()
	}
	return defaultMaxAttempts
}
