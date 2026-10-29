package workflow_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

func TestNormalizeTemplate(t *testing.T) {
	t.Run("fills defaults", func(t *testing.T) {
		tpl := team()
		tpl.DefaultMaxAttempts, tpl.MaxParallelTasks, tpl.MaxAgents = 0, 0, 0
		require.NoError(t, workflow.NormalizeTemplate(tpl))
		assert.Equal(t, uint32(3), tpl.GetDefaultMaxAttempts())
		assert.Equal(t, uint32(1), tpl.GetMaxParallelTasks(), "sequential unless the template says otherwise")
		assert.Equal(t, uint32(8), tpl.GetMaxAgents())
		assert.Equal(t, leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_NONE, tpl.GetSteps()[0].GetApproval())
	})
	for name, mutate := range map[string]func(*leapmuxv1.WorkflowTemplate){
		"needs exactly one lead":   func(tpl *leapmuxv1.WorkflowTemplate) { tpl.Roles[1].IsLead = true },
		"rejects a duplicate role": func(tpl *leapmuxv1.WorkflowTemplate) { tpl.Roles[2].Key = "builder" },
		"rejects a bad role key":   func(tpl *leapmuxv1.WorkflowTemplate) { tpl.Roles[1].Key = "Builder!" },
		"rejects an unknown role":  func(tpl *leapmuxv1.WorkflowTemplate) { tpl.Steps[0].RoleKey = "nobody" },
		"rejects a step for the lead": func(tpl *leapmuxv1.WorkflowTemplate) {
			tpl.Steps[0].RoleKey = "lead"
		},
		"rejects an unknown dependency": func(tpl *leapmuxv1.WorkflowTemplate) { tpl.Steps[1].DependsOn = []string{"nope"} },
		"rejects a cycle":               func(tpl *leapmuxv1.WorkflowTemplate) { tpl.Steps[0].DependsOn = []string{"merge"} },
		"rejects an unknown output":     func(tpl *leapmuxv1.WorkflowTemplate) { tpl.Roles[1].RequiredOutputs = []string{"vibes"} },
		"rejects a pinned placement without a computer": func(tpl *leapmuxv1.WorkflowTemplate) {
			tpl.Roles[1].Agent.Placement.WorkerId = ""
		},
		"rejects a capability placement without capabilities": func(tpl *leapmuxv1.WorkflowTemplate) {
			tpl.Roles[2].Agent.Placement.RequiredCapabilities = nil
		},
		"rejects a role with neither profile nor provider": func(tpl *leapmuxv1.WorkflowTemplate) {
			tpl.Roles[2].Agent.AgentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED
		},
	} {
		t.Run(name, func(t *testing.T) {
			tpl := team()
			mutate(tpl)
			assert.ErrorIs(t, workflow.NormalizeTemplate(tpl), workflow.ErrInvalid)
		})
	}
}

func TestResolveRoles(t *testing.T) {
	profile := &leapmuxv1.AgentProfile{
		Id: "p1", Name: "Codex builder", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Options: map[string]string{"model": "gpt-x", "effort": "medium"}, Instructions: "Profile rules.",
		Placement: &leapmuxv1.WorkerPlacement{Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_ANY_WORKER},
	}
	tpl := team()
	tpl.Roles[1].ProfileId = "p1"
	tpl.Roles[1].Agent = &leapmuxv1.AgentProfile{Options: map[string]string{"effort": "high"}, Instructions: "Role rules."}
	require.NoError(t, workflow.NormalizeTemplate(tpl))

	pinned := &leapmuxv1.WorkerPlacement{Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER, WorkerId: builderPC}
	roles, err := workflow.ResolveRoles(tpl, []*leapmuxv1.AgentProfile{profile}, []*leapmuxv1.WorkflowRoleOverride{
		{RoleKey: "builder", Placement: pinned},
	})
	require.NoError(t, err)
	builder := roles[1].GetAgent()
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, builder.GetAgentProvider(), "from the profile")
	assert.Equal(t, "gpt-x", builder.GetOptions()["model"], "kept from the profile")
	assert.Equal(t, "high", builder.GetOptions()["effort"], "the template overrides the profile")
	assert.True(t, proto.Equal(pinned, builder.GetPlacement()), "the launch overrides the template")
	assert.Equal(t, "Profile rules.\n\nRole rules.", builder.GetInstructions())
	assert.Empty(t, builder.GetId(), "the resolved role holds no reference to follow")

	_, err = workflow.ResolveRoles(tpl, nil, nil)
	assert.ErrorIs(t, err, workflow.ErrInvalid, "a missing profile is an error, not a default")
	_, err = workflow.ResolveRoles(tpl, []*leapmuxv1.AgentProfile{profile}, []*leapmuxv1.WorkflowRoleOverride{{RoleKey: "ghost"}})
	assert.ErrorIs(t, err, workflow.ErrInvalid)
}

func TestResolveRolesDropsOptionsOfAnotherProvider(t *testing.T) {
	tpl := team()
	require.NoError(t, workflow.NormalizeTemplate(tpl))
	roles, err := workflow.ResolveRoles(tpl, nil, []*leapmuxv1.WorkflowRoleOverride{
		{RoleKey: "builder", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE},
	})
	require.NoError(t, err)
	assert.Empty(t, roles[1].GetAgent().GetOptions(), "Codex option ids mean nothing to Claude Code")
}

func TestPlace(t *testing.T) {
	workers := []workflow.WorkerCandidate{
		{ID: "a", Name: "Alpha", Online: true, Capabilities: []string{"os:linux"}},
		{ID: "m", Name: "Mac", Online: true, Capabilities: []string{"os:darwin", "xcode"}},
		{ID: "z", Name: "Zulu", Online: false},
	}
	specific := func(id string) *leapmuxv1.WorkerPlacement {
		return &leapmuxv1.WorkerPlacement{Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER, WorkerId: id}
	}

	got, err := workflow.Place(specific("m"), workers, "")
	require.NoError(t, err)
	assert.Equal(t, "m", got.WorkerID)

	got, err = workflow.Place(specific("z"), workers, "a")
	require.NoError(t, err)
	assert.Empty(t, got.WorkerID, "an offline pinned computer is never replaced")
	assert.Contains(t, got.Waiting, "Zulu")

	_, err = workflow.Place(specific("ghost"), workers, "")
	assert.Error(t, err, "a computer the user does not own can never be used")

	capability := &leapmuxv1.WorkerPlacement{Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_CAPABILITY, RequiredCapabilities: []string{"xcode"}}
	got, err = workflow.Place(capability, workers, "")
	require.NoError(t, err)
	assert.Equal(t, "m", got.WorkerID)

	capability.RequiredCapabilities = []string{"gpu"}
	got, err = workflow.Place(capability, workers, "")
	require.NoError(t, err)
	assert.Empty(t, got.WorkerID)
	assert.True(t, strings.Contains(got.Waiting, "gpu") && strings.Contains(got.Waiting, "offline"), got.Waiting)

	anyWorker := &leapmuxv1.WorkerPlacement{Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_ANY_WORKER}
	got, err = workflow.Place(anyWorker, workers, "m")
	require.NoError(t, err)
	assert.Equal(t, "m", got.WorkerID, "a role keeps the computer it used while it qualifies")
	got, err = workflow.Place(anyWorker, workers, "")
	require.NoError(t, err)
	assert.Equal(t, "a", got.WorkerID, "otherwise the first by name")

	anyWorker.AllowedWorkerIds = []string{"z"}
	got, err = workflow.Place(anyWorker, workers, "")
	require.NoError(t, err)
	assert.Empty(t, got.WorkerID, "an allow-list is never widened")
}

func TestAllowsWorker(t *testing.T) {
	mac := workflow.WorkerCandidate{ID: "m", Name: "Mac", Online: true, Capabilities: []string{"os:darwin", "xcode"}}
	assert.NoError(t, workflow.AllowsWorker(&leapmuxv1.WorkerPlacement{
		Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER, WorkerId: "a",
	}, mac), "the user may move a pinned role")
	assert.Error(t, workflow.AllowsWorker(&leapmuxv1.WorkerPlacement{
		Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_CAPABILITY, RequiredCapabilities: []string{"os:windows"},
	}, mac))
	assert.Error(t, workflow.AllowsWorker(&leapmuxv1.WorkerPlacement{
		Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_ANY_WORKER, AllowedWorkerIds: []string{"a"},
	}, mac))
}

func TestBundleRoundTrip(t *testing.T) {
	for _, format := range []leapmuxv1.WorkflowExportFormat{
		leapmuxv1.WorkflowExportFormat_WORKFLOW_EXPORT_FORMAT_YAML, leapmuxv1.WorkflowExportFormat_WORKFLOW_EXPORT_FORMAT_JSON,
	} {
		t.Run(format.String(), func(t *testing.T) {
			tpl := team()
			require.NoError(t, workflow.NormalizeTemplate(tpl))
			doc, err := workflow.EncodeBundle(&leapmuxv1.WorkflowTemplateBundle{FormatVersion: 1, Template: tpl}, format)
			require.NoError(t, err)
			back, err := workflow.DecodeBundle(doc)
			require.NoError(t, err)
			assert.True(t, proto.Equal(tpl, back.GetTemplate()), "the document carries the whole template")
		})
	}
	_, err := workflow.DecodeBundle("format_version: 99\ntemplate: {name: x}\n")
	assert.ErrorIs(t, err, workflow.ErrInvalid, "a newer format is refused rather than half-read")
	_, err = workflow.DecodeBundle("not: [valid")
	assert.ErrorIs(t, err, workflow.ErrInvalid)
}

func TestLibraryImportExportAndExample(t *testing.T) {
	db := openDB(t)
	e := newEngine(t, db, newFakePlatform())
	ctx := t.Context()

	example, err := e.InstallExample(ctx, userID)
	require.NoError(t, err)
	assert.Len(t, example.GetProfiles(), 4)
	assert.Len(t, example.GetTemplate().GetSteps(), 4)

	exported, err := e.Export(ctx, userID, example.GetTemplate().GetId(), leapmuxv1.WorkflowExportFormat_WORKFLOW_EXPORT_FORMAT_YAML)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(exported.GetFileName(), ".workflow.yaml"))

	copied, err := e.Import(ctx, userID, exported.GetDocument())
	require.NoError(t, err)
	assert.NotEqual(t, example.GetTemplate().GetId(), copied.GetTemplate().GetId(), "an import never overwrites")
	for i, r := range copied.GetTemplate().GetRoles() {
		if r.GetProfileId() != "" {
			assert.NotEqual(t, example.GetTemplate().GetRoles()[i].GetProfileId(), r.GetProfileId(), "profiles are copied too")
		}
	}

	lib, err := e.Library(ctx, userID)
	require.NoError(t, err)
	assert.Len(t, lib.GetTemplates(), 2)
	assert.Len(t, lib.GetProfiles(), 8)

	other, err := e.Library(ctx, "user-2")
	require.NoError(t, err)
	assert.Empty(t, other.GetTemplates(), "a library is per user")

	err = e.DeleteProfile(ctx, userID, copied.GetProfiles()[0].GetId())
	assert.ErrorIs(t, err, workflow.ErrConflict, "a profile a template uses cannot be deleted")

	saved, err := e.SaveTemplate(ctx, userID, copied.GetTemplate())
	require.NoError(t, err)
	assert.Equal(t, uint32(2), saved.GetVersion(), "saving bumps the version a run records")
}
