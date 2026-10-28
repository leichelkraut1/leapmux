package codexobserve

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreshnessDoesNotAdvanceOnCacheReads(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	original := agent.NativeServiceObservation{
		SourceID: "source", Available: true, Partial: true, Sessions: []agent.NativeServiceSessionObservation{{
			SessionID: testThread, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
			StatusObservedAt: now, SourceVerifiedAt: now.Add(time.Second), ExpiresAt: now.Add(freshFor),
		}},
	}
	first := copyObservations([]agent.NativeServiceObservation{original}, now.Add(20*time.Second))
	require.Len(t, first, 1)
	assert.Equal(t, original.Sessions[0].StatusObservedAt, first[0].Sessions[0].StatusObservedAt)
	assert.Equal(t, original.Sessions[0].SourceVerifiedAt, first[0].Sessions[0].SourceVerifiedAt)
	expired := copyObservations([]agent.NativeServiceObservation{original}, now.Add(freshFor))
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED, expired[0].Sessions[0].ActivityState)
	// Returned slices do not alias the live cache.
	first[0].Sessions[0].DisplayName = "mutated"
	assert.Empty(t, original.Sessions[0].DisplayName)
}
func TestContinuousContentPatchesVerifySourceWithoutInventingStatusEvidence(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	state := &watchState{clientID: testClient, followed: map[string]*followed{
		testThread: {candidate: candidate{ID: testThread}, owner: testOwner, revision: -1},
	}}
	snapshot, err := readFrame(framedText(snapshotText(`{"type":"active","activeFlags":[]}`)))
	require.NoError(t, err)
	require.NoError(t, state.acceptState(snapshot, now))
	update := snapshot
	update.Change = change{Kind: "patches", BaseRevision: 3, Revision: 4, Patches: []patch{{Path: []string{"turns"}, Op: "replace"}}}
	require.NoError(t, state.acceptState(update, now.Add(10*time.Second)))
	follow := state.followed[testThread]
	assert.Equal(t, now, follow.statusAt)
	assert.Equal(t, now.Add(10*time.Second), follow.verifiedAt)
	assert.Equal(t, now.Add(10*time.Second+freshFor), follow.expiresAt)
	update.Change = change{Kind: "patches", BaseRevision: 4, Revision: 5, Patches: []patch{{
		Op: "replace", Path: []string{"threadRuntimeStatus"}, ValueRead: true,
		Runtime: runtimeStatus{Kind: "active", HasFlags: true, Flags: []string{"waitingOnApproval"}},
	}}}
	require.NoError(t, state.acceptState(update, now.Add(11*time.Second)))
	assert.Equal(t, now.Add(11*time.Second), follow.statusAt)
	assert.Equal(t, "waiting", follow.state.status())
	update.Change.BaseRevision = 2
	require.Error(t, state.acceptState(update, now.Add(12*time.Second)))
	assert.False(t, follow.valid)
	snapshot.Change.Revision = 9
	require.NoError(t, state.acceptState(snapshot, now.Add(13*time.Second)))
	assert.True(t, follow.valid)
	assert.Equal(t, int64(9), follow.revision)
}
func TestObservationUsesServiceIdentityAndExplicitCoverage(t *testing.T) {
	now := time.Now()
	state := &watchState{
		peer:     PeerIdentity{PID: 42, ProcessStartTime: 123, ExecutableName: "ChatGPT.exe", SourceID: "peer"},
		sourceID: "peer-generation", initialized: true, candidatesOK: true,
		candidates: candidateSet{Candidates: []candidate{{ID: testThread}}, Truncated: true},
		followed: map[string]*followed{testThread: {
			candidate: candidate{ID: testThread, Name: "Named chat"}, valid: true,
			state:     projection{ID: testThread, Resume: "resumed", HasRuntime: true, Runtime: runtimeStatus{Kind: "idle"}},
			expiresAt: now.Add(freshFor),
		}},
	}
	result := state.observation(now)
	assert.Equal(t, int32(42), result.ServicePID)
	assert.Equal(t, int64(123), result.ProcessStartTime)
	assert.True(t, result.Partial)
	assert.True(t, result.CandidateTruncated)
	assert.Equal(t, maxSubscriptions, result.SubscriptionLimit)
	require.Len(t, result.Sessions, 1)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, result.Sessions[0].ActivityState)
	state.candidatesOK = false
	result = state.observation(now)
	assert.False(t, result.Available)
}

func readTestMessage(conn net.Conn) (map[string]any, error) {
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint32(header[:])
	if length > 8192 {
		return nil, fmt.Errorf("unexpected metadata frame size")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, err
	}
	var message map[string]any
	err := json.Unmarshal(data, &message)
	return message, err
}
func replyToTestRequest(conn net.Conn, request map[string]any, owner string, result map[string]any) error {
	return writeFrame(conn, map[string]any{
		"type": "response", "requestId": request["requestId"], "method": request["method"],
		"resultType": "success", "handledByClientId": owner, "result": result,
	})
}
func TestWatcherIsLazyAndUnfollowsOnCloseOrDashboardIdle(t *testing.T) {
	for _, idle := range []bool{false, true} {
		t.Run(fmt.Sprintf("idle=%v", idle), func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { assert.NoError(t, server.Close()) }()
			now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
			var clock atomic.Int64
			clock.Store(now.UnixNano())
			ticks := make(chan time.Time)
			var connects atomic.Int32
			var verifies atomic.Int32
			watcher := newWatcher(dependencies{
				now:   func() time.Time { return time.Unix(0, clock.Load()) },
				tick:  func() (<-chan time.Time, func()) { return ticks, func() {} },
				paths: func() (string, string, error) { return "home", "metadata", nil },
				connect: func(context.Context, string) (net.Conn, PeerIdentity, error) {
					connects.Add(1)
					return client, PeerIdentity{PID: 42, ProcessStartTime: 123, ExecutableName: "ChatGPT.exe", SourceID: "source"}, nil
				},
				verify: func(context.Context, net.Conn, PeerIdentity) error { verifies.Add(1); return nil },
				candidates: func(context.Context, string) (candidateSet, error) {
					return candidateSet{Candidates: []candidate{{ID: testThread, Name: "Named chat"}}}, nil
				},
			})
			defer func() { assert.NoError(t, watcher.Close()) }()
			assert.Zero(t, connects.Load())
			ready := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				init, err := readTestMessage(server)
				if err != nil {
					result <- err
					return
				}
				if init["method"] != "initialize" {
					result <- fmt.Errorf("unexpected initialization")
					return
				}
				params, _ := init["params"].(map[string]any)
				if params["clientType"] != "leapmux-observer" {
					result <- fmt.Errorf("dishonest client type")
					return
				}
				if err = replyToTestRequest(server, init, testClient, map[string]any{"clientId": testClient}); err != nil {
					result <- err
					return
				}
				discover, err := readTestMessage(server)
				if err != nil {
					result <- err
					return
				}
				if discover["method"] != "thread-owner-discovery" {
					result <- fmt.Errorf("unexpected request")
					return
				}
				if err = replyToTestRequest(server, discover, testOwner, map[string]any{"supportsUntrustedAppInput": true}); err != nil {
					result <- err
					return
				}
				follow, err := readTestMessage(server)
				if err != nil {
					result <- err
					return
				}
				params, _ = follow["params"].(map[string]any)
				if follow["method"] != "thread-stream-following-changed" || params["following"] != true {
					result <- fmt.Errorf("missing passive follow")
					return
				}
				close(ready)
				unfollow, err := readTestMessage(server)
				if err != nil {
					result <- err
					return
				}
				params, _ = unfollow["params"].(map[string]any)
				if unfollow["method"] != "thread-stream-following-changed" || params["following"] != false {
					result <- fmt.Errorf("missing passive cleanup")
					return
				}
				result <- nil
			}()
			assert.Empty(t, watcher.Inspect(context.Background()))
			select {
			case <-ready:
			case err := <-result:
				require.NoError(t, err)
				t.Fatal("handshake ended early")
			}
			assert.Equal(t, int32(1), connects.Load())
			if idle {
				clock.Store(now.Add(demandFor).UnixNano())
				ticks <- now.Add(demandFor)
				require.NoError(t, <-result)
				watcher.mu.Lock()
				done := watcher.done
				watcher.mu.Unlock()
				<-done
			} else {
				require.NoError(t, watcher.Close())
				require.NoError(t, <-result)
			}
			assert.GreaterOrEqual(t, verifies.Load(), int32(2))
			watcher.mu.Lock()
			assert.False(t, watcher.running)
			watcher.mu.Unlock()
		})
	}
}

func TestHeartbeatRequiresSameOwnerAndContinuousValidStream(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	f := followed{owner: testOwner, valid: true, statusAt: now, verifiedAt: now, expiresAt: now.Add(freshFor)}
	require.NoError(t, f.verifyOwner(testOwner, now.Add(time.Second)))
	assert.Equal(t, now, f.statusAt)
	assert.Equal(t, now.Add(time.Second), f.verifiedAt)
	f.invalidate(now.Add(2 * time.Second))
	require.NoError(t, f.verifyOwner(testOwner, now.Add(3*time.Second)))
	assert.Equal(t, now.Add(time.Second), f.verifiedAt)
	assert.Equal(t, now.Add(2*time.Second), f.expiresAt)
	f.valid = true
	require.ErrorIs(t, f.verifyOwner(testClient, now.Add(4*time.Second)), errProtocol)
	assert.False(t, f.valid)
	assert.Equal(t, testOwner, f.owner)
}

func TestSnapshotRecoveryCannotRegressPastSeenOwnerRevision(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	state := &watchState{clientID: testClient, followed: map[string]*followed{
		testThread: {candidate: candidate{ID: testThread}, owner: testOwner, revision: -1},
	}}
	snapshot, err := readFrame(framedText(snapshotText(`{"type":"active","activeFlags":[]}`)))
	require.NoError(t, err)
	require.NoError(t, state.acceptState(snapshot, now))
	gap := snapshot
	gap.Change = change{Kind: "patches", BaseRevision: 5, Revision: 6}
	require.ErrorIs(t, state.acceptState(gap, now.Add(time.Second)), errProtocol)
	follow := state.followed[testThread]
	require.False(t, follow.valid)
	assert.Equal(t, int64(3), follow.revision)
	assert.Equal(t, int64(6), follow.snapshotRevisionFloor)
	for _, revision := range []int64{2, 3, 4, 5} {
		stale := snapshot
		stale.Change.Revision = revision
		require.ErrorIs(t, state.acceptState(stale, now.Add(2*time.Second)), errProtocol)
		assert.False(t, follow.valid)
		assert.Equal(t, int64(6), follow.snapshotRevisionFloor)
	}
	recovered := snapshot
	recovered.Change.Revision = 6
	require.NoError(t, state.acceptState(recovered, now.Add(3*time.Second)))
	assert.True(t, follow.valid)
	assert.Equal(t, int64(6), follow.revision)
	assert.Equal(t, int64(6), follow.snapshotRevisionFloor)

	// Even a contiguous unsupported status patch raises the recovery floor:
	// its newer state cannot be replaced with the older accepted snapshot.
	bad := snapshot
	bad.Change = change{Kind: "patches", BaseRevision: 6, Revision: 7, Patches: []patch{{
		Op: "replace", Path: []string{"threadRuntimeStatus", "future"}, ValueRead: true,
	}}}
	require.ErrorIs(t, state.acceptState(bad, now.Add(4*time.Second)), errProtocol)
	require.ErrorIs(t, state.acceptState(recovered, now.Add(5*time.Second)), errProtocol)
	assert.False(t, follow.valid)
	assert.Equal(t, int64(7), follow.snapshotRevisionFloor)
	recovered.Change.Revision = 7
	require.NoError(t, state.acceptState(recovered, now.Add(6*time.Second)))
	assert.True(t, follow.valid)
}
