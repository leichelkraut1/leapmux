//go:build unix

package codebuddy

import (
	"context"
	"slices"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CodeBuddy Code updates its own install unless a truthy DISABLE_AUTOUPDATER
// says otherwise (BooleanUtils.isTruthy). The `-p` mode that the worker starts
// skips the updater today, but a daemon (CODEBUDDY_SESSION_KIND=daemon) runs it,
// so the launch pins the switch off after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	runs := agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "codebuddy", Start: Start, Pins: []string{"DISABLE_AUTOUPDATER=1"},
	})
	for _, run := range runs {
		assert.Contains(t, run.Args, "--include-partial-messages")
	}
}

func TestCodebuddySwarmLaunchArguments(t *testing.T) {
	for _, tc := range []struct {
		value, resume string
		want          bool
	}{
		{}, {value: "false"}, {value: "true", want: true}, {value: "true", resume: "existing-session", want: true},
	} {
		runs := agenttest.RunLaunch(t, agenttest.LaunchProbe{Binary: "codebuddy", Start: func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
			opts.Options = map[string]string{"swarmMode": tc.value}
			opts.ResumeSessionID = tc.resume
			return Start(ctx, opts, sink)
		}})
		require.Len(t, runs, 1)
		assert.Equal(t, tc.want, slices.Contains(runs[0].Args, "--swarm"))
	}
}
