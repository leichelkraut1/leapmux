package claude

import (
	"context"
	"os"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/shirou/gopsutil/v4/process"
)

func lookupClaudeNativeControlSession(ctx context.Context, id string) (agent.NativeSessionObservation, error) {
	observations, err := observeNativeSessions(ctx)
	if err != nil {
		return agent.NativeSessionObservation{}, agent.ErrNativeControlUnavailable
	}
	var match agent.NativeSessionObservation
	count := 0
	for _, observation := range observations {
		if observation.SessionID == id {
			match = observation
			count++
		}
	}
	if count != 1 || match.PID <= 0 || match.PID == os.Getpid() {
		return match, agent.ErrNativeControlUnavailable
	}
	return match, nil
}

// Bind the documented session mapping to the same native creation identity
// before and after a second lookup. No arguments, environment or history reads.
func verifyClaudeNativeControlOwner(ctx context.Context, sessionID string) (nativeControlOwner, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*nativeSessionsTimeout+time.Second)
	defer cancel()
	first, err := lookupClaudeNativeControlSession(ctx, sessionID)
	if err != nil {
		return nativeControlOwner{}, err
	}
	before, ok := procutil.IdentifyProcess(first.PID)
	if !ok || !nativeControlSameUser(ctx, first.PID) {
		return nativeControlOwner{}, agent.ErrNativeControlUnavailable
	}
	p, err := process.NewProcessWithContext(ctx, int32(first.PID))
	if err != nil {
		return nativeControlOwner{}, agent.ErrNativeControlUnavailable
	}
	name, nameErr := p.NameWithContext(ctx)
	executable, exeErr := p.ExeWithContext(ctx)
	return verifyClaudeControlExecutable(ctx, first, before, name, executable, nameErr, exeErr)
}

func claudeNativeControlExecutable(pid int, name, executable string) bool {
	if encodedPID, updated := claudeUpdatedExecutablePID(name); updated && encodedPID != pid {
		return false
	}
	return recognizeNativeProcess(name, executable) == leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI
}

func verifyClaudeControlExecutable(ctx context.Context, first agent.NativeSessionObservation, before procutil.ProcessIdentity,
	name, executable string, nameErr, exeErr error,
) (nativeControlOwner, error) {
	if nameErr != nil || exeErr != nil || !claudeNativeControlExecutable(first.PID, name, executable) {
		return nativeControlOwner{}, agent.ErrNativeControlUnavailable
	}
	second, err := lookupClaudeNativeControlSession(ctx, first.SessionID)
	if err != nil || second.PID != first.PID {
		return nativeControlOwner{}, agent.ErrNativeControlStale
	}
	after, ok := procutil.IdentifyProcess(first.PID)
	if !ok || before != after || !nativeControlSameUser(ctx, first.PID) {
		return nativeControlOwner{}, agent.ErrNativeControlStale
	}
	return nativeControlOwner{identity: after, activity: second.ActivityState}, nil
}
