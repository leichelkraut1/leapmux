package agent

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// InspectExistingNativeSessionControl never constructs a provider bridge. The
// existence bit distinguishes an unsupported/unseen owner from a failed probe
// of an already enrolled controller, which cannot prove that its owner is gone.
func (m *Manager) InspectExistingNativeSessionControl(ctx context.Context, provider leapmuxv1.AgentProvider, sessionID string) (NativeSessionControlState, bool, error) {
	if err := ctx.Err(); err != nil {
		return NativeSessionControlState{}, false, err
	}
	m.nativeControlMu.Lock()
	if err := ctx.Err(); err != nil {
		m.nativeControlMu.Unlock()
		return NativeSessionControlState{}, false, err
	}
	if m.nativeControlClosed {
		m.nativeControlMu.Unlock()
		return NativeSessionControlState{}, true, ErrNativeControlUnavailable
	}
	entry := m.nativeControllers[provider]
	m.nativeControlMu.Unlock()
	if entry == nil {
		return NativeSessionControlState{}, false, ErrNativeControlUnavailable
	}
	entry.mu.Lock()
	known := entry.sessions[sessionID]
	entry.mu.Unlock()
	if !known {
		// Plugins can enroll before the first viewer inspection. Consult only
		// this already-created controller; an unrelated historical session must
		// not construct or enroll a bridge merely because resume was requested.
		if registry, ok := entry.controller.(NativeSessionEnrollmentRegistry); ok {
			known = registry.HasNativeSessionEnrollment(sessionID)
			if known {
				entry.mu.Lock()
				entry.sessions[sessionID] = true
				entry.mu.Unlock()
			}
		}
	}
	if !known {
		return NativeSessionControlState{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return NativeSessionControlState{}, true, err
	}
	state, err := entry.controller.Inspect(ctx, sessionID)
	return state, true, err
}
