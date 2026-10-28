package service

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
)

const maxNativeSessions = 256
const maxNativeSessionDisplayNameRunes = 160

var nativeSessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type nativeSessionIdentitySource interface {
	ObservationIdentity(context.Context, int) (procutil.ProcessIdentity, error)
}

// observeNativeSessions queries only providers already present in the same-user
// external inventory. Failed observers preserve the process-only fallback. No
// session is started, resumed, attached for control, or read for conversation data.
func observeNativeSessions(ctx context.Context, source nativeSessionIdentitySource, result *leapmuxv1.InspectNativeAgentProcessesResponse, identities map[int]procutil.ProcessIdentity, registry *agent.Registry) {
	if ctx.Err() != nil {
		return
	}
	processes := make(map[leapmuxv1.AgentProvider][]*leapmuxv1.NativeAgentProcess)
	for _, process := range result.Processes {
		reg, ok := registry.Registration(process.AgentProvider)
		identity, verified := identities[int(process.Pid)]
		if !ok || reg.ObserveNativeSessions == nil || !verified || identity.IsZero() || identity.StartTime <= 0 {
			continue
		}
		processes[process.AgentProvider] = append(processes[process.AgentProvider], process)
	}
	type providerResult struct {
		provider     leapmuxv1.AgentProvider
		observations []agent.NativeSessionObservation
	}
	// Each observer owns only its private result. Cancellation returns promptly
	// while provider process cleanup can finish without mutating a sent response.
	results := make(chan providerResult, len(processes))
	for provider := range processes {
		reg, _ := registry.Registration(provider)
		go func() {
			observations, err := reg.ObserveNativeSessions(ctx)
			if err != nil {
				observations = nil
			}
			results <- providerResult{provider: provider, observations: observations}
		}()
	}
	for range len(processes) {
		select {
		case <-ctx.Done():
			return
		case observed := <-results:
			if ctx.Err() != nil {
				return
			}
			applyNativeSessions(ctx, source, processes[observed.provider], identities, observed.observations)
		}
	}
}

// applyNativeSessions verifies that each candidate still names the same process
// after querying. Duplicate IDs are discarded, preventing contradictory states
// from becoming a plausible status. The limit bounds metadata sent to a client.
func applyNativeSessions(ctx context.Context, source nativeSessionIdentitySource, processes []*leapmuxv1.NativeAgentProcess, before map[int]procutil.ProcessIdentity, observations []agent.NativeSessionObservation) {
	live := make(map[int]*leapmuxv1.NativeAgentProcess)
	for _, process := range processes {
		pid := int(process.Pid)
		identity, err := source.ObservationIdentity(ctx, pid)
		if err == nil && !identity.IsZero() && identity == before[pid] {
			live[pid] = process
		}
	}
	duplicates := make(map[string]int)
	for _, observation := range observations {
		duplicates[observation.SessionID]++
	}
	published := 0
	for _, observation := range observations {
		if ctx.Err() != nil || published >= maxNativeSessions {
			return
		}
		process := live[observation.PID]
		if process == nil || duplicates[observation.SessionID] != 1 || !nativeSessionID.MatchString(observation.SessionID) {
			continue
		}
		state := observation.ActivityState
		switch state {
		case leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
			leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
			leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER:
		default:
			state = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
		}
		process.Sessions = append(process.Sessions, &leapmuxv1.NativeAgentSession{
			SessionId:     observation.SessionID,
			ActivityState: state,
			DisplayName:   normalizeNativeSessionDisplayName(observation.DisplayName),
		})
		published++
	}
}

// normalizeNativeSessionDisplayName keeps provider labels readable and bounded.
// Names are untrusted plain text, never markup or a source of inferred content.
func normalizeNativeSessionDisplayName(name string) string {
	normalized := strings.Join(strings.FieldsFunc(name, func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsControl(char) || unicode.In(char, unicode.Cf)
	}), " ")
	runes := []rune(normalized)
	if len(runes) > maxNativeSessionDisplayNameRunes {
		runes = runes[:maxNativeSessionDisplayNameRunes]
	}
	return string(runes)
}
