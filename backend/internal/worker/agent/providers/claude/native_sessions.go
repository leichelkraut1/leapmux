package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	nativeSessionsTimeout   = 3 * time.Second
	maxNativeSessionsOutput = 512 << 10
)

var errNativeSessionsUnavailable = errors.New("native Claude session status is unavailable")

// nativeSessionStatus allows the documented display name, but omits directories
// and conversation data. The service bounds names before publishing matched rows.
type nativeSessionStatus struct {
	PID        int             `json:"pid"`
	SessionID  string          `json:"sessionId"`
	Kind       string          `json:"kind"`
	State      string          `json:"state"`
	Status     string          `json:"status"`
	WaitingFor string          `json:"waitingFor"`
	Name       json.RawMessage `json:"name"`
}

// observeNativeSessions uses Claude's supported status command, never its private
// session files. The caller verifies process ownership and excludes managed agents.
// https://code.claude.com/docs/en/agent-view#read-session-state-from-a-script
func observeNativeSessions(ctx context.Context) ([]agent.NativeSessionObservation, error) {
	return observeNativeSessionsWithRunner(ctx, runNativeSessionCLI)
}

// Prefer the native executable so periodic reads do not pay shell startup costs.
// A failed native invocation is not retried through a shell.
func runNativeSessionCLI(ctx context.Context, run providerkit.CLIRun) ([]byte, error) {
	return runNativeSessionCLIWithRunners(ctx, run, providerkit.RunNativeCLI, providerkit.RunCLI)
}

func runNativeSessionCLIWithRunners(ctx context.Context, run providerkit.CLIRun,
	nativeRun func(context.Context, providerkit.NativeCLIRun) ([]byte, error),
	shellRun func(context.Context, providerkit.CLIRun) ([]byte, error),
) ([]byte, error) {
	output, err := nativeRun(ctx, providerkit.NativeCLIRun{
		Program: "claude", Label: run.Label, Args: run.Args,
		WorkingDir: run.WorkingDir, Timeout: run.Timeout, MaxOutput: run.MaxOutput,
	})
	if errors.Is(err, providerkit.ErrNativeCLIUnavailable) {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return shellRun(ctx, run)
	}
	return output, err
}

func observeNativeSessionsWithRunner(ctx context.Context, run func(context.Context, providerkit.CLIRun) ([]byte, error)) ([]agent.NativeSessionObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, nativeSessionsTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	output, err := run(ctx, providerkit.CLIRun{
		Locator:    claudeLocator,
		Label:      "Claude",
		Args:       []string{"agents", "--json"},
		WorkingDir: os.TempDir(),
		Timeout:    nativeSessionsTimeout,
		MaxOutput:  maxNativeSessionsOutput,
	})
	if err != nil {
		// RunCLI errors can contain CLI stderr, including user paths or login
		// diagnostics. Do not pass that text into the inventory or its logs.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errNativeSessionsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return parseNativeSessions(output)
}

func parseNativeSessions(output []byte) ([]agent.NativeSessionObservation, error) {
	if len(output) > maxNativeSessionsOutput {
		return nil, errNativeSessionsUnavailable
	}
	var sessions []nativeSessionStatus
	if err := json.Unmarshal(output, &sessions); err != nil || sessions == nil {
		return nil, errNativeSessionsUnavailable
	}
	// A process cannot identify two live sessions in the same snapshot. Refuse
	// ambiguous records instead of assigning one session's state to another.
	counts := make(map[int]int, len(sessions))
	for _, session := range sessions {
		counts[session.PID]++
	}
	result := make([]agent.NativeSessionObservation, 0, len(sessions))
	for _, session := range sessions {
		if session.PID <= 0 || counts[session.PID] != 1 || !validNativeSessionID(session.SessionID) {
			continue
		}
		activity := nativeSessionActivity(session)
		if activity == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED {
			continue
		}
		// An absent or malformed optional name must not hide valid status.
		var displayName string
		_ = json.Unmarshal(session.Name, &displayName)
		result = append(result, agent.NativeSessionObservation{
			PID:           session.PID,
			SessionID:     session.SessionID,
			DisplayName:   displayName,
			ActivityState: activity,
		})
	}
	return result, nil
}

func validNativeSessionID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func nativeSessionActivity(session nativeSessionStatus) leapmuxv1.AgentActivityState {
	unknown := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
	working := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
	waiting := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
	idle := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE

	// Claude documents each recognized waiting reason as blocked on user input.
	// Unknown future reasons must not become "Waiting on you".
	if session.Status == "waiting" {
		switch session.WaitingFor {
		case "", "permission prompt", "input needed", "sandbox request", "worker request", "dialog open":
		default:
			return unknown
		}
	}
	switch session.Kind {
	case "background":
		// Background work can continue between tool calls or scheduled steps
		// while its process is idle. Its task state is the authoritative signal.
		switch session.State {
		case "working":
			if session.Status == "waiting" {
				return waiting
			}
			return working
		case "blocked":
			return waiting
		case "done":
			return idle
		default:
			return unknown
		}
	case "interactive":
		switch session.Status {
		case "busy":
			return working
		case "waiting":
			return waiting
		case "idle":
			return idle
		default:
			return unknown
		}
	default:
		return unknown
	}
}
