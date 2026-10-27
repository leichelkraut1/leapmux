package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserveNativeSessionsCommandContract(t *testing.T) {
	t.Parallel()
	require.NotNil(t, Registration().ObserveNativeSessions)
	result, err := observeNativeSessionsWithRunner(context.Background(), func(ctx context.Context, run providerkit.CLIRun) ([]byte, error) {
		assert.True(t, run.Locator.Valid())
		assert.Equal(t, claudeLocator, run.Locator)
		assert.Equal(t, []string{"agents", "--json"}, run.Args)
		assert.Equal(t, 3*time.Second, run.Timeout)
		assert.Equal(t, 512<<10, run.MaxOutput)
		assert.False(t, run.LoginShell, "a status query must not add a login profile")
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), 3*time.Second)
		return []byte(`[{"pid":42,"sessionId":"session-42","kind":"interactive","status":"idle"}]`), nil
	})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, result[0].ActivityState)
}

func TestObserveNativeSessionsDoesNotExposeCommandDiagnostics(t *testing.T) {
	t.Parallel()
	result, err := observeNativeSessionsWithRunner(context.Background(), func(context.Context, providerkit.CLIRun) ([]byte, error) {
		return []byte("private stdout"), errors.New("private stderr with credentials")
	})
	require.ErrorIs(t, err, errNativeSessionsUnavailable)
	assert.Nil(t, result)
	assert.NotContains(t, err.Error(), "private")
	assert.NotContains(t, err.Error(), "credentials")
}

func TestObserveNativeSessionsHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := observeNativeSessionsWithRunner(ctx, func(context.Context, providerkit.CLIRun) ([]byte, error) {
		t.Fatal("a canceled query must not execute the CLI")
		return nil, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)
}

func TestObserveNativeSessionsDiscardsLateResults(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := observeNativeSessionsWithRunner(ctx, func(context.Context, providerkit.CLIRun) ([]byte, error) {
		cancel()
		return []byte(`[{"pid":42,"sessionId":"session-42","kind":"interactive","status":"idle"}]`), nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)
}

func TestParseNativeSessionsKeepsOnlyStatusMetadata(t *testing.T) {
	t.Parallel()
	result, err := parseNativeSessions([]byte(`[
		{"pid":42,"sessionId":"session-42","kind":"interactive","status":"busy",
		 "cwd":"/private/project","name":"private conversation","transcript":"private content","token":"secret"},
		{"pid":43,"sessionId":"session-43","kind":"interactive","status":"waiting","waitingFor":"permission prompt"}
	]`))
	require.NoError(t, err)
	assert.Equal(t, []agent.NativeSessionObservation{
		{PID: 42, SessionID: "session-42", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
		{PID: 43, SessionID: "session-43", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER},
	}, result)
}

func TestNativeSessionActivity(t *testing.T) {
	t.Parallel()
	unknown := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
	working := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
	waiting := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
	idle := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE
	for _, tc := range []struct {
		name    string
		session nativeSessionStatus
		want    leapmuxv1.AgentActivityState
	}{
		{"busy", nativeSessionStatus{Kind: "interactive", Status: "busy"}, working},
		{"idle", nativeSessionStatus{Kind: "interactive", Status: "idle"}, idle},
		{"permission", nativeSessionStatus{Kind: "interactive", Status: "waiting", WaitingFor: "permission prompt"}, waiting},
		{"question", nativeSessionStatus{Kind: "interactive", Status: "waiting", WaitingFor: "input needed"}, waiting},
		{"sandbox", nativeSessionStatus{Kind: "interactive", Status: "waiting", WaitingFor: "sandbox request"}, waiting},
		{"dialog", nativeSessionStatus{Kind: "interactive", Status: "waiting", WaitingFor: "dialog open"}, waiting},
		{"unspecified wait reason", nativeSessionStatus{Kind: "interactive", Status: "waiting"}, waiting},
		{"worker request", nativeSessionStatus{Kind: "interactive", Status: "waiting", WaitingFor: "worker request"}, waiting},
		{"future wait reason", nativeSessionStatus{Kind: "interactive", Status: "waiting", WaitingFor: "future"}, unknown},
		{"future status", nativeSessionStatus{Kind: "interactive", Status: "future"}, unknown},
		{"undocumented shell status", nativeSessionStatus{Kind: "interactive", Status: "shell"}, unknown},
		{"missing kind", nativeSessionStatus{Status: "idle"}, unknown},
		{"future kind", nativeSessionStatus{Kind: "future", Status: "idle"}, unknown},
		{"background between steps", nativeSessionStatus{Kind: "background", State: "working", Status: "idle"}, working},
		{"background permission", nativeSessionStatus{Kind: "background", State: "working", Status: "waiting", WaitingFor: "permission prompt"}, waiting},
		{"background blocked", nativeSessionStatus{Kind: "background", State: "blocked", Status: "idle"}, waiting},
		{"background worker request", nativeSessionStatus{Kind: "background", State: "blocked", Status: "waiting", WaitingFor: "worker request"}, waiting},
		{"background done", nativeSessionStatus{Kind: "background", State: "done", Status: "idle"}, idle},
		{"background failed", nativeSessionStatus{Kind: "background", State: "failed", Status: "idle"}, unknown},
		{"background stopped", nativeSessionStatus{Kind: "background", State: "stopped", Status: "idle"}, unknown},
		{"background unknown state", nativeSessionStatus{Kind: "background", State: "future", Status: "busy"}, unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nativeSessionActivity(tc.session))
		})
	}
}

func TestParseNativeSessionsRejectsMalformedOrOversizedOutput(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"", "null", "{}", "[] []", "[", `[{"pid":"42"}]`,
		strings.Repeat(" ", maxNativeSessionsOutput+1),
	} {
		result, err := parseNativeSessions([]byte(input))
		require.ErrorIs(t, err, errNativeSessionsUnavailable)
		assert.Nil(t, result)
	}
	result, err := parseNativeSessions([]byte("[]"))
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestParseNativeSessionsOmitsInvalidOrAmbiguousIdentities(t *testing.T) {
	t.Parallel()
	result, err := parseNativeSessions([]byte(`[
		{"pid":0,"sessionId":"zero","kind":"interactive","status":"idle"},
		{"pid":-1,"sessionId":"negative","kind":"interactive","status":"idle"},
		{"pid":1,"kind":"interactive","status":"idle"},
		{"pid":2,"sessionId":"../../private","kind":"interactive","status":"idle"},
		{"pid":3,"sessionId":"first","kind":"interactive","status":"busy"},
		{"pid":3,"sessionId":"second","kind":"interactive","status":"idle"},
		{"pid":4,"sessionId":"future","kind":"interactive","status":"future"},
		{"pid":5,"sessionId":"valid","kind":"interactive","status":"idle"}
	]`))
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, 5, result[0].PID)
	assert.Equal(t, "valid", result[0].SessionID)
	assert.False(t, validNativeSessionID(strings.Repeat("x", 129)))
	assert.False(t, validNativeSessionID("a\nb"))
	assert.False(t, validNativeSessionID("☃"))
}

func TestObserveNativeSessionsPrefersDirectExecutable(t *testing.T) {
	t.Parallel()
	result, err := observeNativeSessionsWithRunner(context.Background(), func(ctx context.Context, run providerkit.CLIRun) ([]byte, error) {
		return runNativeSessionCLIWithRunners(ctx, run, func(nativeCtx context.Context, native providerkit.NativeCLIRun) ([]byte, error) {
			assert.Same(t, ctx, nativeCtx)
			assert.Equal(t, "claude", native.Program)
			assert.Equal(t, "Claude", native.Label)
			assert.Equal(t, []string{"agents", "--json"}, native.Args)
			assert.Equal(t, run.WorkingDir, native.WorkingDir)
			assert.Equal(t, nativeSessionsTimeout, native.Timeout)
			assert.Equal(t, maxNativeSessionsOutput, native.MaxOutput)
			return []byte("[{\"pid\":42,\"sessionId\":\"session-42\",\"kind\":\"interactive\",\"status\":\"idle\"}]"), nil
		}, func(context.Context, providerkit.CLIRun) ([]byte, error) {
			t.Fatal("a native result must not launch a shell")
			return nil, nil
		})
	})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, 42, result[0].PID)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, result[0].ActivityState)
}

func TestNativeSessionCLIFallsBackOnlyForUnavailableExecutable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		nativeErr error
		wantShell bool
	}{
		{"not resolved", providerkit.ErrNativeCLIUnavailable, true},
		{"start or exit failure", errors.New("native invocation failed"), false},
		{"deadline", context.DeadlineExceeded, false},
		{"success", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), nativeSessionsTimeout)
			defer cancel()
			run := providerkit.CLIRun{Locator: claudeLocator, Args: []string{"agents", "--json"}}
			shellCalls := 0
			output, err := runNativeSessionCLIWithRunners(ctx, run,
				func(context.Context, providerkit.NativeCLIRun) ([]byte, error) {
					return []byte("native"), tc.nativeErr
				},
				func(shellCtx context.Context, shell providerkit.CLIRun) ([]byte, error) {
					shellCalls++
					assert.Same(t, ctx, shellCtx, "fallback must keep the original deadline")
					assert.Equal(t, claudeLocator, shell.Locator)
					assert.Equal(t, run.Args, shell.Args)
					return []byte("shell"), nil
				})
			if tc.wantShell {
				require.NoError(t, err)
				assert.Equal(t, 1, shellCalls)
				assert.Equal(t, "shell", string(output))
			} else {
				assert.Equal(t, 0, shellCalls)
				assert.Equal(t, tc.nativeErr, err)
				assert.Equal(t, "native", string(output))
			}
		})
	}
}

func TestNativeSessionCLIDoesNotFallbackAfterCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output, err := runNativeSessionCLIWithRunners(ctx, providerkit.CLIRun{},
		func(context.Context, providerkit.NativeCLIRun) ([]byte, error) {
			cancel()
			return nil, providerkit.ErrNativeCLIUnavailable
		},
		func(context.Context, providerkit.CLIRun) ([]byte, error) {
			t.Fatal("canceled native lookup must not start a shell")
			return nil, nil
		})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, output)
}
