package codexobserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type controlDependencies struct {
	now        func() time.Time
	connect    func(context.Context, string) (net.Conn, PeerIdentity, error)
	verify     func(context.Context, net.Conn, PeerIdentity) error
	verifyPath func(string) error
	home       func() (string, error)
}

type controlLease struct {
	thread  string
	owner   string
	peer    PeerIdentity
	expires time.Time
}

// Controller uses the existing authenticated Desktop/IDE IPC router only. It
// does not create an endpoint, enumerate chats, read stored histories or resume.
type Controller struct {
	gate    chan struct{}
	mu      sync.Mutex
	closed  bool
	active  net.Conn
	epoch   string
	leases  map[string]controlLease
	deps    controlDependencies
	journal controlJournal
}

var _ agent.NativeSessionController = (*Controller)(nil)

func NewController(options agent.NativeSessionControllerOptions) *Controller {
	return newController(options, controlDependencies{now: time.Now, connect: Connect, verify: VerifyPeer, verifyPath: controlPrivatePath,
		home: func() (string, error) { home, _, err := metadataPaths(); return home, err }})
}

func newController(options agent.NativeSessionControllerOptions, deps controlDependencies) *Controller {
	return &Controller{gate: make(chan struct{}, 1), epoch: newID(), leases: make(map[string]controlLease), deps: deps,
		journal: controlJournal{directory: options.Directory, verify: deps.verifyPath, ensureDirectory: controlPrivateDirectory, syncDirectory: controlSyncDirectory}}
}

func (c *Controller) acquire(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.gate <- struct{}{}:
	}
	if ctx.Err() != nil {
		<-c.gate
		return ctx.Err()
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		<-c.gate
		return agent.ErrNativeControlUnavailable
	}
	return nil
}

func (c *Controller) prepareJournal() error {
	now := c.deps.now()
	protected := make(map[string]bool)
	for token, lease := range c.leases {
		if now.Before(lease.expires) {
			protected[controlBindingHash(token)] = true
		}
	}
	return c.journal.prepare(now, protected)
}

func (c *Controller) open(ctx context.Context, thread string) (*controlConnection, controlSnapshot, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, controlSnapshot{}, agent.ErrNativeControlUnavailable
	}
	home, err := c.deps.home()
	if err != nil {
		return nil, controlSnapshot{}, agent.ErrNativeControlUnavailable
	}
	conn, peer, err := c.deps.connect(ctx, home)
	if err != nil {
		return nil, controlSnapshot{}, agent.ErrNativeControlUnavailable
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return nil, controlSnapshot{}, agent.ErrNativeControlUnavailable
	}
	c.active = conn
	c.mu.Unlock()
	s := &controlConnection{conn: conn, peer: peer, verify: c.deps.verify, thread: thread}
	s.stopCancellation = context.AfterFunc(ctx, func() { _ = conn.Close() })
	if c.deps.verify(ctx, conn, peer) != nil {
		c.release(s)
		return nil, controlSnapshot{}, agent.ErrNativeControlUnavailable
	}
	if s.initialize(ctx) != nil {
		c.release(s)
		return nil, controlSnapshot{}, agent.ErrNativeControlUnavailable
	}
	state, err := s.snapshot(ctx)
	if err != nil {
		return s, controlSnapshot{}, err
	}
	return s, state, nil
}

func (c *Controller) release(s *controlConnection) {
	s.close()
	c.mu.Lock()
	if c.active == s.conn {
		c.active = nil
	}
	c.mu.Unlock()
}

func (c *Controller) Inspect(ctx context.Context, id string) (agent.NativeSessionControlState, error) {
	if !uuidPattern.MatchString(id) {
		return agent.NativeSessionControlState{}, agent.ErrNativeControlInvalid
	}
	if err := c.acquire(ctx); err != nil {
		return agent.NativeSessionControlState{SessionID: id}, err
	}
	defer func() { <-c.gate }()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s, snapshot, err := c.open(ctx, id)
	initial := agent.NativeSessionControlState{SessionID: id, UnavailableReason: "A verified running Codex app or IDE must own this chat."}
	if s != nil {
		defer c.release(s)
		initial.OwnerPresent = s.owner != "" && uuidPattern.MatchString(s.owner)
	}
	if err != nil {
		if errors.Is(err, errControlNoOwner) {
			return initial, nil
		}
		if ctx.Err() != nil {
			return initial, ctx.Err()
		}
		return initial, agent.ErrNativeControlUnavailable
	}
	now := c.deps.now()
	state := agent.NativeSessionControlState{SessionID: id, OwnerLabel: "Codex native owner", TransportLabel: "Codex local IPC",
		InputBehavior: leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_SEND_NOW, ObservedAt: now, OwnerPresent: true}
	state.ActivityState, state.UnavailableReason = snapshot.activity()
	if state.UnavailableReason != "" {
		return state, nil
	}
	if c.prepareJournal() != nil {
		state.UnavailableReason = "Private delivery tracking is unavailable on this Worker."
		return state, nil
	}
	for token, lease := range c.leases {
		if !now.Before(lease.expires) || (lease.thread == id && (lease.owner != s.owner || lease.peer != s.peer)) {
			delete(c.leases, token)
		}
	}
	if len(c.leases) >= 256 {
		state.UnavailableReason = "Too many native chat leases are active."
		return state, nil
	}
	state.BindingToken = c.epoch + "-" + newID()
	state.ExpiresAt = now.Add(time.Duration(contracts.NativeSessionControlLeaseSeconds) * time.Second)
	c.leases[state.BindingToken] = controlLease{thread: id, owner: s.owner, peer: s.peer, expires: state.ExpiresAt}
	state.CanSend = true
	state.ActiveTurnID = snapshot.activeTurn()
	state.CanInterrupt = snapshot.Runtime.Type == "active" && state.ActiveTurnID != ""
	return state, nil
}

func (s controlSnapshot) activity() (leapmuxv1.AgentActivityState, string) {
	switch s.Runtime.Type {
	case "idle":
		if len(s.Runtime.Flags) != 0 {
			return leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED, "The native Codex state version is unsupported."
		}
		return leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, ""
	case "active":
		state := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
		for _, flag := range s.Runtime.Flags {
			if flag != "waitingOnApproval" && flag != "waitingOnUserInput" {
				return leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED, "The native Codex state version is unsupported."
			}
			state = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
		}
		return state, ""
	default:
		return leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED, "The native Codex chat is not ready for input."
	}
}

func (c *Controller) binding(token, thread string) (controlLease, error) {
	lease, ok := c.leases[token]
	if !ok || !strings.HasPrefix(token, c.epoch+"-") || lease.thread != thread || !c.deps.now().Before(lease.expires) {
		return controlLease{}, agent.ErrNativeControlStale
	}
	return lease, nil
}

func (c *Controller) mutate(ctx context.Context, id, thread, token string, payload any, apply func(context.Context, *controlConnection, controlSnapshot) error) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-c.gate }()
	if !uuidPattern.MatchString(id) || !uuidPattern.MatchString(thread) {
		return agent.ErrNativeControlInvalid
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.prepareJournal() != nil {
		return agent.ErrNativeControlUnavailable
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return agent.ErrNativeControlInvalid
	}
	digest := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(digest[:])
	previous, err := c.journal.existing(id, fingerprint)
	if err != nil {
		return err
	}
	if previous != nil {
		return controlReplay(*previous)
	}
	lease, err := c.binding(token, thread)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s, state, err := c.open(ctx, thread)
	if s != nil {
		defer c.release(s)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return agent.ErrNativeControlStale
	}
	if lease.owner != s.owner || lease.peer != s.peer || !c.deps.now().Before(lease.expires) {
		return agent.ErrNativeControlStale
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, reason := state.activity(); reason != "" {
		return agent.ErrNativeControlUnavailable
	}
	if err := c.journal.claim(id, fingerprint, token, lease.expires); err != nil {
		return err
	}
	result := apply(ctx, s, state)
	return c.journal.finish(id, fingerprint, token, lease.expires, result)
}

func (c *Controller) Send(ctx context.Context, input agent.NativeSessionInput) (agent.NativeSessionInputReceipt, error) {
	if input.Behavior != leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_SEND_NOW ||
		!utf8.ValidString(input.Text) || strings.TrimSpace(input.Text) == "" || len(input.Text) > contracts.NativeSessionControlMaxTextBytes || strings.ContainsRune(input.Text, 0) {
		return agent.NativeSessionInputReceipt{}, agent.ErrNativeControlInvalid
	}
	err := c.mutate(ctx, input.RequestID, input.SessionID, input.BindingToken, struct {
		Kind  string
		Input agent.NativeSessionInput
	}{"send-now", input}, func(ctx context.Context, s *controlConnection, _ controlSnapshot) error {
		result, err := s.request(ctx, "thread-follower-start-turn", 2, map[string]any{"conversationId": input.SessionID, "turnStart": map[string]any{
			"request": map[string]any{"threadId": input.SessionID, "clientUserMessageId": input.RequestID,
				"input": []any{map[string]any{"type": "text", "text": input.Text, "text_elements": []any{}}}},
			"context": map[string]any{"inheritThreadSettings": true}}}, s.owner)
		if err != nil {
			return err
		}
		var response struct {
			Result struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			} `json:"result"`
		}
		if json.Unmarshal(result, &response) != nil || !uuidPattern.MatchString(response.Result.Turn.ID) {
			return agent.ErrNativeControlUncertain
		}
		return nil
	})
	if err != nil {
		return agent.NativeSessionInputReceipt{}, err
	}
	return agent.NativeSessionInputReceipt{Disposition: leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_ACCEPTED,
		Detail: "Sent to the existing Codex chat. Input can join its active turn."}, nil
}

func (c *Controller) Interrupt(ctx context.Context, input agent.NativeSessionInterrupt) error {
	if !uuidPattern.MatchString(input.ExpectedTurnID) {
		return agent.ErrNativeControlInvalid
	}
	return c.mutate(ctx, input.RequestID, input.SessionID, input.BindingToken, struct {
		Kind  string
		Input agent.NativeSessionInterrupt
	}{"interrupt", input}, func(ctx context.Context, s *controlConnection, state controlSnapshot) error {
		if state.Runtime.Type != "active" || state.activeTurn() != input.ExpectedTurnID {
			return agent.ErrNativeControlStale
		}
		result, err := s.request(ctx, "thread-follower-interrupt-turn", 4, map[string]any{"conversationId": input.SessionID, "mode": "user-stop", "expectedTurnId": input.ExpectedTurnID}, s.owner)
		if err != nil {
			return err
		}
		var response struct {
			OK          bool   `json:"ok"`
			Interrupted string `json:"interruptedTurnId"`
		}
		if json.Unmarshal(result, &response) != nil {
			return agent.ErrNativeControlUncertain
		}
		if !response.OK || response.Interrupted != input.ExpectedTurnID {
			return agent.ErrNativeControlStale
		}
		return nil
	})
}

func (c *Controller) Close() error {
	c.mu.Lock()
	c.closed = true
	conn := c.active
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}
