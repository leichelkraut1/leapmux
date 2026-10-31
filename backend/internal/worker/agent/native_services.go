package agent

import (
	"context"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// NativeServiceObserver observes an existing user-owned app service. It cannot
// start providers or control chats. A Manager owns one instance per provider;
// Inspect renews dashboard demand, and Close releases every passive subscription.
type NativeServiceObserver interface {
	Inspect(context.Context) []NativeServiceObservation
	Close() error
}

// NativeServiceObservation identifies an OS-verified service, not a chat's agent
// process. ServicePID must never be used as a session PID or process ownership claim.
type NativeServiceObservation struct {
	SourceID           string
	ServicePID         int32
	ProcessStartTime   int64
	ExecutableName     string
	Available          bool
	Partial            bool
	CandidateCount     int
	CandidateTruncated bool
	SubscriptionLimit  int
	Sessions           []NativeServiceSessionObservation
}

// NativeServiceSessionObservation is runtime evidence from a continuously
// followed owner. Cache reads never refresh any timestamp.
type NativeServiceSessionObservation struct {
	SessionID        string
	DisplayName      string
	ActivityState    leapmuxv1.AgentActivityState
	StatusObservedAt time.Time
	SourceVerifiedAt time.Time
	ExpiresAt        time.Time
}
