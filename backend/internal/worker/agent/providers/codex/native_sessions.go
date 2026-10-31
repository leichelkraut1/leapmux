package codex

import (
	"context"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/agentreport"
)

// observeNativeSessions reads only the user's explicit opt-in status reports.
// It never reads Codex runtime files, conversation history, or credentials.
func observeNativeSessions(ctx context.Context) ([]agent.NativeSessionObservation, error) {
	dir, err := agentreport.Directory()
	if err != nil {
		return nil, err
	}
	return observeNativeSessionsInDirectory(ctx, dir, time.Now())
}

func observeNativeSessionsInDirectory(ctx context.Context, dir string, now time.Time) ([]agent.NativeSessionObservation, error) {
	reports, err := agentreport.Read(ctx, dir, now)
	if err != nil {
		return nil, err
	}
	observations := make([]agent.NativeSessionObservation, 0, len(reports))
	for _, report := range reports {
		state := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
		switch report.State {
		case "working":
			state = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
		case "waiting":
			state = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
		case "idle":
			state = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE
		}
		observations = append(observations, agent.NativeSessionObservation{
			PID: report.PID, ProcessStartTime: report.ProcessStartTime,
			SessionID: report.SessionID, DisplayName: report.DisplayName,
			ActivityState: state, AgentReported: true, ReportedAt: report.ReportedAt,
		})
	}
	return observations, nil
}
