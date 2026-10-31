package codexobserve

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

const controlTestThread = "11111111-1111-4111-8111-111111111111"
const controlTestOwner = "22222222-2222-4222-8222-222222222222"
const controlTestTurn = "33333333-3333-4333-8333-333333333333"

type controlTestServer struct {
	mu                sync.Mutex
	owner             string
	version           int
	active            bool
	runtimeType       string
	runtimeFlags      []string
	inputTexts        []string
	noop              bool
	cut               bool
	noOwner           bool
	sessionID         string
	snapshotGate      <-chan struct{}
	snapshotRequested chan struct{}
	snapshotOnce      sync.Once
	mutations         int
	targets           []string
	threads           []string
}

func (s *controlTestServer) read(conn net.Conn) (map[string]json.RawMessage, error) {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > controlOutboundLimit {
		return nil, io.ErrUnexpectedEOF
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(conn, raw); err != nil {
		return nil, err
	}
	var frame map[string]json.RawMessage
	err := json.Unmarshal(raw, &frame)
	return frame, err
}

func testString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func (s *controlTestServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	client := "44444444-4444-4444-8444-444444444444"
	for {
		request, err := s.read(conn)
		if err != nil {
			return
		}
		method := testString(request["method"])
		var params map[string]json.RawMessage
		_ = json.Unmarshal(request["params"], &params)
		s.mu.Lock()
		owner, version, active, noop, cut, noOwner := s.owner, s.version, s.active, s.noop, s.cut, s.noOwner
		s.mu.Unlock()
		if method == "thread-stream-following-changed" {
			var following bool
			_ = json.Unmarshal(params["following"], &following)
			if !following {
				continue
			}
			s.mu.Lock()
			sessionID, gate, requested := s.sessionID, s.snapshotGate, s.snapshotRequested
			runtimeType, runtimeFlags := s.runtimeType, append([]string{}, s.runtimeFlags...)
			s.mu.Unlock()
			if sessionID == "" {
				sessionID = controlTestThread
			}
			if gate != nil {
				if requested != nil {
					s.snapshotOnce.Do(func() { close(requested) })
				}
				<-gate
			}
			runtime := "idle"
			turnStatus := "completed"
			if active {
				runtime = "active"
				turnStatus = "inProgress"
			}
			if runtimeType != "" {
				runtime = runtimeType
			}
			state := map[string]any{"id": controlTestThread, "sessionId": sessionID, "resumeState": "resumed",
				"threadRuntimeStatus": map[string]any{"type": runtime, "activeFlags": runtimeFlags},
				"turnHistory": map[string]any{"kind": "canonical", "history": map[string]any{"entitiesByKey": map[string]any{
					"generated-turn": map[string]any{"turnId": controlTestTurn, "status": turnStatus,
						"items": []any{map[string]any{"type": "agentMessage", "text": "PRIVATE_GENERATED_TRANSCRIPT"}}}}}}}
			frame := map[string]any{"type": "broadcast", "method": "thread-stream-state-changed", "sourceClientId": owner,
				"targetClientIds": []string{client}, "version": version, "params": map[string]any{"hostId": "local", "conversationId": controlTestThread,
					"change": map[string]any{"type": "snapshot", "revision": 1, "conversationState": state}}}
			if writeFrame(conn, frame) != nil {
				return
			}
			continue
		}
		if testString(request["type"]) != "request" {
			continue
		}
		response := map[string]any{"type": "response", "method": method, "requestId": testString(request["requestId"]), "resultType": "success", "handledByClientId": owner}
		switch method {
		case "initialize":
			response["handledByClientId"] = client
			response["result"] = map[string]any{"clientId": client}
		case "thread-owner-discovery":
			if noOwner {
				response["resultType"] = "error"
				response["error"] = "no-client-found"
			} else {
				response["result"] = map[string]any{"supportsUntrustedAppInput": true}
			}
		case "thread-follower-start-turn", "thread-follower-interrupt-turn":
			s.mu.Lock()
			s.mutations++
			s.targets = append(s.targets, testString(request["targetClientId"]))
			s.threads = append(s.threads, testString(params["conversationId"]))
			s.mu.Unlock()
			if cut {
				return
			}
			if method == "thread-follower-start-turn" {
				var turnStart struct {
					Request struct {
						Input []struct {
							Text string `json:"text"`
						} `json:"input"`
					} `json:"request"`
				}
				_ = json.Unmarshal(params["turnStart"], &turnStart)
				s.mu.Lock()
				for _, item := range turnStart.Request.Input {
					s.inputTexts = append(s.inputTexts, item.Text)
				}
				s.mu.Unlock()
				response["result"] = map[string]any{"result": map[string]any{"turn": map[string]any{"id": controlTestTurn}}}
			} else {
				result := map[string]any{"ok": true}
				if !noop {
					result["interruptedTurnId"] = testString(params["expectedTurnId"])
				}
				response["result"] = result
			}
		default:
			return
		}
		if writeFrame(conn, response) != nil {
			return
		}
	}
}

func controlFixture(t *testing.T) (*Controller, *controlTestServer, agent.NativeSessionControllerOptions, controlDependencies) {
	t.Helper()
	server := &controlTestServer{owner: controlTestOwner, version: 11}
	peer := PeerIdentity{PID: 17, ProcessStartTime: 23, ExecutableName: "Code.exe", SourceID: "generated-peer"}
	deps := controlDependencies{now: time.Now, home: func() (string, error) { return "/generated", nil }, verifyPath: controlPrivatePath,
		verify: func(context.Context, net.Conn, PeerIdentity) error { return nil },
		connect: func(context.Context, string) (net.Conn, PeerIdentity, error) {
			a, b := net.Pipe()
			go server.serve(b)
			return a, peer, nil
		}}
	opts := agent.NativeSessionControllerOptions{Directory: filepath.Join(t.TempDir(), "worker-data", "native-control", "AGENT_PROVIDER_CODEX")}
	controller := newController(opts, deps)
	t.Cleanup(func() { require.NoError(t, controller.Close()) })
	return controller, server, opts, deps
}

func controlInput(state agent.NativeSessionControlState) agent.NativeSessionInput {
	return agent.NativeSessionInput{SessionID: controlTestThread, BindingToken: state.BindingToken, RequestID: "55555555-5555-4555-8555-555555555555",
		Text: "PRIVATE_GENERATED_INPUT", Behavior: leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_SEND_NOW}
}

func TestNativeControlExistingOwnerSendNow(t *testing.T) {
	c, server, opts, _ := controlFixture(t)
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.True(t, state.CanSend)
	require.True(t, state.OwnerPresent)
	require.Regexp(t, `^[A-Za-z0-9_.-]+$`, state.BindingToken)
	require.NoError(t, controlPrivatePath(filepath.Join(opts.Directory, ".codex-control-ledger")))
	require.Equal(t, leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_SEND_NOW, state.InputBehavior)
	input := controlInput(state)
	receipt, err := c.Send(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_ACCEPTED, receipt.Disposition)
	_, err = c.Send(context.Background(), input)
	require.NoError(t, err)
	server.mu.Lock()
	require.Equal(t, 1, server.mutations)
	require.Equal(t, []string{controlTestOwner}, server.targets)
	require.Equal(t, []string{controlTestThread}, server.threads)
	server.mu.Unlock()
	files, err := os.ReadDir(opts.Directory)
	require.NoError(t, err)
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(opts.Directory, file.Name()))
		require.NoError(t, err)
		require.NotContains(t, string(raw), input.Text)
		require.NotContains(t, string(raw), "PRIVATE_GENERATED_TRANSCRIPT")
	}
}

func TestNativeControlOwnerChangeRefused(t *testing.T) {
	c, server, _, _ := controlFixture(t)
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	server.mu.Lock()
	server.owner = "66666666-6666-4666-8666-666666666666"
	server.mu.Unlock()
	_, err = c.Send(context.Background(), controlInput(state))
	require.ErrorIs(t, err, agent.ErrNativeControlStale)
	server.mu.Lock()
	require.Zero(t, server.mutations)
	server.mu.Unlock()
}

func TestNativeControlVersionFailsClosed(t *testing.T) {
	c, server, _, _ := controlFixture(t)
	server.mu.Lock()
	server.version = 12
	server.mu.Unlock()
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	require.True(t, state.OwnerPresent)
	require.False(t, state.CanSend)
	require.Empty(t, state.BindingToken)
}

func TestNativeControlBehaviorRequired(t *testing.T) {
	c, server, _, _ := controlFixture(t)
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	for _, behavior := range []leapmuxv1.NativeSessionInputBehavior{leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_UNSPECIFIED,
		leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_QUEUE_NEXT_TURN} {
		input := controlInput(state)
		input.Behavior = behavior
		_, err = c.Send(context.Background(), input)
		require.ErrorIs(t, err, agent.ErrNativeControlInvalid)
	}
	server.mu.Lock()
	require.Zero(t, server.mutations)
	server.mu.Unlock()
}

func TestNativeControlInterruptNoopIsStale(t *testing.T) {
	c, server, _, _ := controlFixture(t)
	server.mu.Lock()
	server.active = true
	server.noop = true
	server.mu.Unlock()
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.True(t, state.CanInterrupt)
	require.Equal(t, controlTestTurn, state.ActiveTurnID)
	input := agent.NativeSessionInterrupt{SessionID: controlTestThread, BindingToken: state.BindingToken, RequestID: "77777777-7777-4777-8777-777777777777", ExpectedTurnID: controlTestTurn}
	require.ErrorIs(t, c.Interrupt(context.Background(), input), agent.ErrNativeControlStale)
	require.ErrorIs(t, c.Interrupt(context.Background(), input), agent.ErrNativeControlStale)
	server.mu.Lock()
	require.Equal(t, 1, server.mutations)
	server.mu.Unlock()
}

func TestNativeControlUncertainNeverReplaysAcrossRestart(t *testing.T) {
	c, server, opts, deps := controlFixture(t)
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	server.mu.Lock()
	server.cut = true
	server.mu.Unlock()
	input := controlInput(state)
	_, err = c.Send(context.Background(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlUncertain)
	_, err = c.Send(context.Background(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlUncertain)
	next := newController(opts, deps)
	defer func() { require.NoError(t, next.Close()) }()
	_, err = next.Send(context.Background(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlUncertain)
	input.Text = "changed generated input"
	_, err = next.Send(context.Background(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlInvalid)
	server.mu.Lock()
	require.Equal(t, 1, server.mutations)
	server.mu.Unlock()
}

func TestNativeControlEpochAndExpiryRefuseOldLease(t *testing.T) {
	c, server, opts, deps := controlFixture(t)
	now := time.Now()
	c.deps.now = func() time.Time { return now }
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	input := controlInput(state)
	next := newController(opts, deps)
	defer func() { require.NoError(t, next.Close()) }()
	_, err = next.Send(context.Background(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlStale)
	now = now.Add(time.Minute)
	_, err = c.Send(context.Background(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlStale)
	server.mu.Lock()
	require.Zero(t, server.mutations)
	server.mu.Unlock()
}

func TestNativeControlConfirmedNoOwnerDiffersFromUnknownFailure(t *testing.T) {
	c, server, _, _ := controlFixture(t)
	server.mu.Lock()
	server.noOwner = true
	server.mu.Unlock()
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.False(t, state.OwnerPresent)
	c.deps.connect = func(context.Context, string) (net.Conn, PeerIdentity, error) { return nil, PeerIdentity{}, errPeer }
	state, err = c.Inspect(context.Background(), controlTestThread)
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	require.False(t, state.OwnerPresent)
}

func TestNativeControlCanceledWaitDoesNotTouchJournal(t *testing.T) {
	c, _, opts, _ := controlFixture(t)
	c.gate <- struct{}{}
	defer func() { <-c.gate }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Inspect(ctx, controlTestThread); done <- err }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(30 * time.Second):
		t.Fatal("canceled operation remained behind the gate")
	}
	_, err := os.Lstat(opts.Directory)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestNativeControlPrunesOnlyExpiredTerminalRecords(t *testing.T) {
	c, server, _, _ := controlFixture(t)
	now := time.Now()
	c.deps.now = func() time.Time { return now }
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	input := controlInput(state)
	_, err = c.Send(context.Background(), input)
	require.NoError(t, err)
	_, err = os.Lstat(c.journal.path(input.RequestID))
	require.NoError(t, err)
	now = now.Add(time.Minute)
	_, err = c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	_, err = os.Lstat(c.journal.path(input.RequestID))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = c.Send(context.Background(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlStale)
	server.mu.Lock()
	require.Equal(t, 1, server.mutations)
	server.mu.Unlock()
}

func TestNativeControlFailedClaimFlushNeverWritesMutation(t *testing.T) {
	c, server, _, _ := controlFixture(t)
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	c.journal.syncDirectory = func(string) error { return io.ErrClosedPipe }
	_, err = c.Send(context.Background(), controlInput(state))
	require.ErrorIs(t, err, agent.ErrNativeControlUncertain)
	server.mu.Lock()
	require.Zero(t, server.mutations)
	server.mu.Unlock()
}

func TestNativeControlSessionMismatchPreservesOwnerPresence(t *testing.T) {
	c, server, _, _ := controlFixture(t)
	server.mu.Lock()
	server.sessionID = "99999999-9999-4999-8999-999999999999"
	server.mu.Unlock()
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	require.True(t, state.OwnerPresent)
	require.False(t, state.CanSend)
	require.Empty(t, state.BindingToken)
}

func TestNativeControlCancellationClosesBlockedTransport(t *testing.T) {
	c, server, opts, _ := controlFixture(t)
	gate := make(chan struct{})
	requested := make(chan struct{})
	server.mu.Lock()
	server.snapshotGate = gate
	server.snapshotRequested = requested
	server.mu.Unlock()
	defer close(gate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Inspect(ctx, controlTestThread); done <- err }()
	select {
	case <-requested:
	case <-time.After(30 * time.Second):
		t.Fatal("generated snapshot request was not reached")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(30 * time.Second):
		t.Fatal("cancellation did not close the transport")
	}
	_, err := os.Lstat(opts.Directory)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestNativeControlMaxEscapedInput(t *testing.T) {
	c, server, _, _ := controlFixture(t)
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	input := controlInput(state)
	input.Text = strings.Repeat("\x01", contracts.NativeSessionControlMaxTextBytes-1) + "X"
	encoded, err := json.Marshal(input.Text)
	require.NoError(t, err)
	require.Greater(t, len(encoded), 128<<10)
	receipt, err := c.Send(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_ACCEPTED, receipt.Disposition)
	server.mu.Lock()
	require.Equal(t, 1, server.mutations)
	require.Equal(t, []string{input.Text}, server.inputTexts)
	server.mu.Unlock()
}

func TestNativeControlFreshUnsupportedRuntimeNeverClaims(t *testing.T) {
	for _, test := range []struct {
		name        string
		runtimeType string
		flags       []string
	}{
		{name: "system-error", runtimeType: "systemError"},
		{name: "future-type", runtimeType: "newRuntimeState"},
		{name: "future-active-flag", runtimeType: "active", flags: []string{"newRuntimeFlag"}},
		{name: "inconsistent-idle", runtimeType: "idle", flags: []string{"waitingOnApproval"}},
	} {
		for _, operation := range []string{"send", "interrupt"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				c, server, _, _ := controlFixture(t)
				server.mu.Lock()
				server.active = true
				server.mu.Unlock()
				state, err := c.Inspect(context.Background(), controlTestThread)
				require.NoError(t, err)
				require.True(t, state.CanSend)
				require.True(t, state.CanInterrupt)
				server.mu.Lock()
				server.runtimeType = test.runtimeType
				server.runtimeFlags = test.flags
				server.mu.Unlock()
				input := controlInput(state)
				if operation == "send" {
					_, err = c.Send(context.Background(), input)
				} else {
					err = c.Interrupt(context.Background(), agent.NativeSessionInterrupt{SessionID: input.SessionID,
						BindingToken: input.BindingToken, RequestID: input.RequestID, ExpectedTurnID: controlTestTurn})
				}
				require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
				_, err = os.Lstat(c.journal.path(input.RequestID))
				require.ErrorIs(t, err, os.ErrNotExist)
				server.mu.Lock()
				require.Zero(t, server.mutations)
				server.mu.Unlock()
			})
		}
	}
}

// The owner may consume the request and close the pipe before the read-side
// deadline is installed. That is after delivery, even though Read never runs.
type controlPostWriteDeadlineFailure struct {
	net.Conn
	writes int
}

func (c *controlPostWriteDeadlineFailure) Write(data []byte) (int, error) {
	c.writes++
	return len(data), nil
}

func (c *controlPostWriteDeadlineFailure) SetDeadline(time.Time) error {
	if c.writes >= 2 {
		return io.ErrClosedPipe
	}
	return nil
}

func TestNativeControlPostWriteReadDeadlineIsUncertain(t *testing.T) {
	for _, method := range []string{"thread-follower-start-turn", "thread-follower-interrupt-turn"} {
		t.Run(method, func(t *testing.T) {
			conn := &controlPostWriteDeadlineFailure{}
			wire := &controlConnection{conn: conn, client: controlTestThread, owner: controlTestOwner,
				verify: func(context.Context, net.Conn, PeerIdentity) error { return nil }}
			_, err := wire.request(context.Background(), method, 2, map[string]string{}, controlTestOwner)
			require.ErrorIs(t, err, agent.ErrNativeControlUncertain)
			require.Equal(t, 2, conn.writes)
		})
	}
}
