package agent

import (
	"context"
	"errors"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

var (
	ErrNativeControlUnavailable = errors.New("native chat control is unavailable")
	ErrNativeControlStale       = errors.New("native chat owner or turn changed")
	ErrNativeControlInvalid     = errors.New("invalid native chat control request")
	ErrNativeControlUncertain   = errors.New("native chat delivery could not be confirmed")
)

// NativeSessionController communicates with an already-running native owner.
// Implementations are concurrent, never launch/resume a replacement agent, and
// revalidate the exact binding immediately before every write. Close disconnects
// the bridge; it must not terminate the native app or its conversation.
type NativeSessionController interface {
	Inspect(context.Context, string) (NativeSessionControlState, error)
	Send(context.Context, NativeSessionInput) (NativeSessionInputReceipt, error)
	Interrupt(context.Context, NativeSessionInterrupt) error
	Close() error
}

// Directory is a Worker-selected private leaf, never a browser-supplied path.
// A provider needing a local endpoint must fail closed when it is empty.
type NativeSessionControllerOptions struct {
	Directory string
}

// NativeSessionControlState is a short-lived lease for one enrolled live owner.
// BindingToken is opaque, includes this controller instance's random epoch, and
// is invalidated when the session's owner changes. Saved history is not a lease.
type NativeSessionControlState struct {
	SessionID string
	// Independent of plugin connectivity or write capabilities. A verified
	// original owner prevents a second managed process claiming this session.
	OwnerPresent      bool
	OwnerLabel        string
	TransportLabel    string
	UnavailableReason string
	BindingToken      string
	ActiveTurnID      string
	CanSend           bool
	CanInterrupt      bool
	ActivityState     leapmuxv1.AgentActivityState
	ObservedAt        time.Time
	ExpiresAt         time.Time
	RecentInputs      []NativeSessionInputOutcome
	InputBehavior     leapmuxv1.NativeSessionInputBehavior
}

type NativeSessionInputOutcome struct {
	RequestID string
	State     leapmuxv1.NativeSessionInputState
	Detail    string
	TurnID    string
}

// NativeSessionInput accepts the delivery behavior explicitly chosen by the
// caller. A transport must not replay an unacknowledged write after reconnecting.
type NativeSessionInput struct {
	SessionID    string
	BindingToken string
	RequestID    string
	Text         string
	Behavior     leapmuxv1.NativeSessionInputBehavior
}

type NativeSessionInputReceipt struct {
	Disposition leapmuxv1.NativeSessionInputDisposition
	Detail      string
}

// NativeSessionInterrupt cannot target a newer turn than the one the user saw.
type NativeSessionInterrupt struct {
	SessionID      string
	BindingToken   string
	RequestID      string
	ExpectedTurnID string
}
