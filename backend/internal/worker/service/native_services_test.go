package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testNativeService(now time.Time) agent.NativeServiceObservation {
	return agent.NativeServiceObservation{
		SourceID: "verified-service-generation", ServicePID: 42, ProcessStartTime: now.Add(-time.Hour).UnixMilli(),
		ExecutableName: "ChatGPT.exe", Available: true, Partial: true, CandidateCount: 128, SubscriptionLimit: 8,
		Sessions: []agent.NativeServiceSessionObservation{{
			SessionID: "native-session", DisplayName: "  Name\nwith\u200bspaces  ",
			ActivityState:    leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
			StatusObservedAt: now.Add(-time.Hour), SourceVerifiedAt: now.Add(-time.Second), ExpiresAt: now.Add(44 * time.Second),
		}},
	}
}
func nativeServiceResult(source agent.NativeServiceObservation, now time.Time) *leapmuxv1.InspectNativeAgentProcessesResponse {
	result := &leapmuxv1.InspectNativeAgentProcessesResponse{}
	applyNativeServices(result, map[leapmuxv1.AgentProvider][]agent.NativeServiceObservation{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX: {source},
	}, now)
	return result
}
func TestNativeServicesKeepServiceIdentitySeparateAndDoNotRefreshEvidence(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	source := testNativeService(now)
	result := nativeServiceResult(source, now)
	assert.Empty(t, result.Processes)
	require.Len(t, result.ServiceGroups, 1)
	group := result.ServiceGroups[0]
	assert.Equal(t, int32(42), group.ServicePid)
	assert.True(t, group.Partial)
	require.Len(t, group.Sessions, 1)
	row := group.Sessions[0]
	assert.Equal(t, "Name with spaces", row.DisplayName)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, row.ActivityState)
	assert.Equal(t, timefmt.Format(source.Sessions[0].StatusObservedAt), row.StatusObservedAt)
	assert.Equal(t, timefmt.Format(source.Sessions[0].SourceVerifiedAt), row.SourceVerifiedAt)
	assert.False(t, row.AgentReported)
	assert.Empty(t, row.ReportedAt)
}
func TestNativeServicesInvalidateBadFreshnessAndClampExpiry(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	for _, name := range []string{"stale", "future status", "future verification", "verification before evidence", "missing expiry", "unavailable", "unknown status"} {
		t.Run(name, func(t *testing.T) {
			source := testNativeService(now)
			row := &source.Sessions[0]
			switch name {
			case "stale":
				row.ExpiresAt = now
			case "future status":
				row.StatusObservedAt = now.Add(time.Second)
			case "future verification":
				row.SourceVerifiedAt = now.Add(time.Second)
			case "verification before evidence":
				row.StatusObservedAt = now
			case "missing expiry":
				row.ExpiresAt = time.Time{}
			case "unavailable":
				source.Available = false
			case "unknown status":
				row.ActivityState = leapmuxv1.AgentActivityState(999)
			}
			result := nativeServiceResult(source, now)
			require.Len(t, result.ServiceGroups, 1)
			require.Len(t, result.ServiceGroups[0].Sessions, 1)
			assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED, result.ServiceGroups[0].Sessions[0].ActivityState)
		})
	}
	source := testNativeService(now)
	source.Sessions[0].ExpiresAt = now.Add(24 * time.Hour)
	result := nativeServiceResult(source, now)
	assert.Equal(t, timefmt.Format(source.Sessions[0].SourceVerifiedAt.Add(maxNativeServiceFreshness)), result.ServiceGroups[0].Sessions[0].ExpiresAt)
}
func TestNativeServicesRejectInvalidIdentityDuplicateSessionsAndPrivatePaths(t *testing.T) {
	now := time.Now()
	for _, name := range []string{"path", "source", "pid", "start", "future start"} {
		t.Run(name, func(t *testing.T) {
			source := testNativeService(now)
			switch name {
			case "path":
				source.ExecutableName = "C:\\private\\ChatGPT.exe"
			case "source":
				source.SourceID = "private/path"
			case "pid":
				source.ServicePID = 0
			case "start":
				source.ProcessStartTime = 0
			case "future start":
				source.ProcessStartTime = now.Add(time.Second).UnixMilli()
			}
			assert.Empty(t, nativeServiceResult(source, now).ServiceGroups)
		})
	}
	source := testNativeService(now)
	source.Sessions = append(source.Sessions, source.Sessions[0])
	result := nativeServiceResult(source, now)
	require.Len(t, result.ServiceGroups, 1)
	assert.Empty(t, result.ServiceGroups[0].Sessions)
	source = testNativeService(now)
	observations := map[leapmuxv1.AgentProvider][]agent.NativeServiceObservation{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX: {source, source},
	}
	result = &leapmuxv1.InspectNativeAgentProcessesResponse{}
	applyNativeServices(result, observations, now)
	assert.Empty(t, result.ServiceGroups)
}
func TestNativeServicesCapReturnedMetadata(t *testing.T) {
	now := time.Now()
	source := testNativeService(now)
	base := source.Sessions[0]
	source.Sessions = nil
	for i := 0; i < maxNativeSessions+10; i++ {
		row := base
		row.SessionID = fmt.Sprintf("session-%d", i)
		row.DisplayName = strings.Repeat("a", 200)
		source.Sessions = append(source.Sessions, row)
	}
	source.CandidateCount = 999999
	source.SubscriptionLimit = 999999
	result := nativeServiceResult(source, now)
	require.Len(t, result.ServiceGroups, 1)
	group := result.ServiceGroups[0]
	assert.Len(t, group.Sessions, maxNativeSessions)
	assert.Len(t, group.Sessions[0].DisplayName, 160)
	assert.True(t, group.Partial)
	assert.Equal(t, int32(10000), group.CandidateCount)
	assert.Equal(t, int32(maxNativeSessions), group.SubscriptionLimit)
}
