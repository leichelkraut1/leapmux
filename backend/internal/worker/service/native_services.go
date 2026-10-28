package service

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const maxNativeServiceGroups = 16

// This is a generic upper bound, not the provider's chosen heartbeat interval.
const maxNativeServiceFreshness = time.Minute

var nativeSourceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

// applyNativeServices publishes provider-neutral metadata from an observer that
// authenticated its own service transport. A router PID is never converted into
// an agent process/session PID or matched by a coincidental process basename.
func applyNativeServices(result *leapmuxv1.InspectNativeAgentProcessesResponse, observations map[leapmuxv1.AgentProvider][]agent.NativeServiceObservation, now time.Time) {
	providers := make([]leapmuxv1.AgentProvider, 0, len(observations))
	for provider := range observations {
		providers = append(providers, provider)
	}
	slices.Sort(providers)
	remaining := maxNativeSessions
	for _, provider := range providers {
		if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			continue
		}
		if _, ok := leapmuxv1.AgentProvider_name[int32(provider)]; !ok {
			continue
		}
		sources := observations[provider]
		duplicates := map[string]int{}
		for _, source := range sources {
			duplicates[source.SourceID]++
		}
		for _, source := range sources {
			if len(result.ServiceGroups) >= maxNativeServiceGroups {
				return
			}
			if duplicates[source.SourceID] != 1 || !nativeSourceID.MatchString(source.SourceID) ||
				source.ServicePID <= 0 || source.ProcessStartTime <= 0 || source.ProcessStartTime > now.UnixMilli() ||
				!validServiceExecutable(source.ExecutableName) {
				continue
			}
			group := &leapmuxv1.NativeAgentServiceGroup{
				SourceId: source.SourceID, AgentProvider: provider, ServicePid: source.ServicePID,
				ExecutableName: source.ExecutableName, Available: source.Available, Partial: source.Partial,
				CandidateCount:     int32(min(max(source.CandidateCount, 0), 10000)),
				CandidateTruncated: source.CandidateTruncated,
				SubscriptionLimit:  int32(min(max(source.SubscriptionLimit, 0), maxNativeSessions)),
			}
			ids := map[string]int{}
			for _, session := range source.Sessions {
				ids[session.SessionID]++
			}
			for _, session := range source.Sessions {
				if remaining == 0 {
					group.Partial = true
					break
				}
				if ids[session.SessionID] != 1 || !nativeSessionID.MatchString(session.SessionID) {
					continue
				}
				row := &leapmuxv1.NativeAgentSession{
					SessionId: session.SessionID, DisplayName: normalizeNativeSessionDisplayName(session.DisplayName),
					ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED,
				}
				// Malformed/future evidence is Unknown. Neither DTO projection nor polling
				// changes the timestamp that the provider actually observed.
				valid := !session.StatusObservedAt.IsZero() && !session.SourceVerifiedAt.IsZero() &&
					!session.StatusObservedAt.After(now) && !session.SourceVerifiedAt.After(now) &&
					!session.SourceVerifiedAt.Before(session.StatusObservedAt) &&
					session.ExpiresAt.After(session.SourceVerifiedAt)
				if valid {
					expires := minTime(session.ExpiresAt, session.SourceVerifiedAt.Add(maxNativeServiceFreshness))
					row.StatusObservedAt = timefmt.Format(session.StatusObservedAt)
					row.SourceVerifiedAt = timefmt.Format(session.SourceVerifiedAt)
					row.ExpiresAt = timefmt.Format(expires)
					if group.Available && now.Before(expires) {
						switch session.ActivityState {
						case leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED:
							// Preserve Unknown for an unclassified runtime state.
						case leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
							leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
							leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE:
							row.ActivityState = session.ActivityState
						}
					}
				}
				group.Sessions = append(group.Sessions, row)
				remaining--
			}
			result.ServiceGroups = append(result.ServiceGroups, group)
		}
	}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func validServiceExecutable(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 160 || strings.ContainsAny(name, "/\\:") {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) })
}
