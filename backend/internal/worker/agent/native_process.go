package agent

import (
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// RecognizeNativeProcess delegates executable recognition to the provider.
// executablePath is transient OS metadata and must never enter the wire response.
// An empty path permits a cheap name-only candidate pass before owner inspection.
func (r *Registry) RecognizeNativeProcess(name, executablePath string) (leapmuxv1.AgentProvider, leapmuxv1.NativeAgentProcessKind) {
	for _, provider := range r.providers {
		recognize := r.byProvider[provider].RecognizeNativeProcess
		if recognize == nil {
			continue
		}
		if kind := recognize(name, executablePath); kind != leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED {
			return provider, kind
		}
	}
	return leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED
}

// NativeSessionObservation carries provider observations or explicit agent reports. The
// service matches PID against verified same-user external processes before publishing it.
type NativeSessionObservation struct {
	PID int
	// ProcessStartTime binds a stored report to the original process, in Unix milliseconds.
	ProcessStartTime int64
	// AgentReported distinguishes explicit claims from provider runtime observations.
	AgentReported bool
	ReportedAt    time.Time
	SessionID     string
	DisplayName   string
	ActivityState leapmuxv1.AgentActivityState
}
