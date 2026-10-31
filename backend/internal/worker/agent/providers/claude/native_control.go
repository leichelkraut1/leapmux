package claude

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
)

const (
	nativeControlMaxPeers    = 128
	nativeControlMaxCommands = 2048
	nativeControlMaxBody     = 256 << 10
)

type nativeControlOwner struct {
	identity procutil.ProcessIdentity
	activity leapmuxv1.AgentActivityState
}

type nativeControlPeer struct {
	sessionID  string
	instance   string
	owner      nativeControlOwner
	lastSeen   time.Time
	verifiedAt time.Time
	activeTurn string
	stopped    bool
	commands   []*nativeControlCommand
	byRequest  map[string]*nativeControlCommand
	recent     []agent.NativeSessionInputOutcome
}

type nativeControlCommand struct {
	RequestID   string `json:"requestId"`
	DeliveryID  string `json:"deliveryId"`
	Kind        string `json:"kind"`
	Text        string `json:"text,omitempty"`
	TurnID      string `json:"turnId,omitempty"`
	InstanceID  string `json:"instanceId"`
	state       leapmuxv1.NativeSessionInputState
	detail      string
	fingerprint [32]byte
	done        chan struct{}
	finished    bool
	settledAt   time.Time
	peer        *nativeControlPeer
}

type nativeControlDescriptor struct {
	URL         string `json:"url"`
	Token       string `json:"token"`
	Epoch       string `json:"epoch"`
	WorkerPID   int    `json:"workerPid"`
	WorkerStart int64  `json:"workerStart"`
}

// nativeControlController owns only a loopback bridge, never a Claude process.
// Its private descriptor is read by explicitly enrolled Claude Mods plugins.
type nativeControlController struct {
	mu           sync.Mutex
	clock        quartz.Clock
	directory    string
	epoch        string
	token        string
	peers        map[string]*nativeControlPeer
	server       *http.Server
	listener     net.Listener
	lock         *os.File
	closed       bool
	commandCount int
	initErr      error
	verifyOwner  func(context.Context, string) (nativeControlOwner, error)
}

var _ agent.NativeSessionController = (*nativeControlController)(nil)

func newNativeSessionController(opts agent.NativeSessionControllerOptions) agent.NativeSessionController {
	return newClaudeNativeController(opts, quartz.NewReal(), verifyClaudeNativeControlOwner)
}

func newClaudeNativeController(opts agent.NativeSessionControllerOptions, clock quartz.Clock,
	verify func(context.Context, string) (nativeControlOwner, error),
) *nativeControlController {
	c := &nativeControlController{clock: clock, directory: opts.Directory,
		peers: make(map[string]*nativeControlPeer), verifyOwner: verify}
	c.initErr = c.start()
	return c
}

func nativeControlRandom() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func (c *nativeControlController) start() (err error) {
	if c.directory == "" || !filepath.IsAbs(c.directory) {
		return agent.ErrNativeControlUnavailable
	}
	parent := filepath.Dir(c.directory)
	if err := os.MkdirAll(filepath.Dir(parent), 0o700); err != nil {
		return err
	}
	if err := nativeControlPrivateDirectory(parent); err != nil {
		return err
	}
	if err := nativeControlPrivateDirectory(c.directory); err != nil {
		return err
	}
	c.lock, err = nativeControlLock(filepath.Join(c.directory, "bridge.lock"))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()
	c.epoch, err = nativeControlRandom()
	if err != nil {
		return err
	}
	c.token, err = nativeControlRandom()
	if err != nil {
		return err
	}
	c.listener, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	descriptorPath := filepath.Join(c.directory, "bridge.json")
	if err := c.writePlugin(descriptorPath); err != nil {
		return err
	}
	identity, ok := procutil.IdentifyProcess(os.Getpid())
	if !ok {
		return agent.ErrNativeControlUnavailable
	}
	descriptor, err := json.Marshal(nativeControlDescriptor{
		URL: "http://" + c.listener.Addr().String(), Token: c.token, Epoch: c.epoch,
		WorkerPID: identity.PID, WorkerStart: identity.StartTime,
	})
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(descriptorPath, descriptor, 0o600); err != nil {
		return err
	}
	if err := nativeControlCheckPrivate(descriptorPath); err != nil {
		return err
	}
	c.server = &http.Server{Handler: http.HandlerFunc(c.handle),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second,
		MaxHeaderBytes: 8 << 10}
	go func() { _ = c.server.Serve(c.listener) }()
	return nil
}

func (c *nativeControlController) binding(peer *nativeControlPeer, expires time.Time) string {
	encoded, _ := json.Marshal([]any{c.epoch, peer.sessionID, peer.instance, peer.owner.identity, expires.UnixNano()})
	mac := hmac.New(sha256.New, []byte(c.token))
	_, _ = mac.Write(encoded)
	value := make([]byte, 8, 40)
	binary.BigEndian.PutUint64(value, uint64(expires.UnixNano()))
	value = append(value, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(value)
}

func (c *nativeControlController) validBinding(peer *nativeControlPeer, binding string, now time.Time) bool {
	value, err := base64.RawURLEncoding.DecodeString(binding)
	if err != nil || len(value) != 40 {
		return false
	}
	expires := time.Unix(0, int64(binary.BigEndian.Uint64(value[:8])))
	return now.Before(expires) && !expires.After(now.Add(time.Duration(contracts.NativeSessionControlLeaseSeconds)*time.Second)) &&
		hmac.Equal([]byte(binding), []byte(c.binding(peer, expires)))
}

func (c *nativeControlController) peerFresh(peer *nativeControlPeer, now time.Time) bool {
	lease := time.Duration(contracts.NativeSessionControlLeaseSeconds) * time.Second
	return peer != nil && !peer.lastSeen.After(now) && now.Sub(peer.lastSeen) < lease &&
		!peer.verifiedAt.IsZero() && !peer.verifiedAt.After(now) && now.Sub(peer.verifiedAt) < lease
}

func (c *nativeControlController) Inspect(ctx context.Context, sessionID string) (agent.NativeSessionControlState, error) {
	state := agent.NativeSessionControlState{SessionID: sessionID, OwnerLabel: "Claude Code",
		TransportLabel: "Enrolled Claude plugin", UnavailableReason: "Enable the LeapMux plugin in this Claude chat.",
		InputBehavior: leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_QUEUE_NEXT_TURN}
	if !validNativeSessionID(sessionID) {
		return state, agent.ErrNativeControlInvalid
	}
	c.mu.Lock()
	if c.closed || c.initErr != nil {
		c.mu.Unlock()
		return state, agent.ErrNativeControlUnavailable
	}
	peer := c.peers[sessionID]
	if peer == nil {
		c.mu.Unlock()
		if _, err := c.verifyOwner(ctx, sessionID); err != nil {
			return state, agent.ErrNativeControlUnavailable
		}
		state.OwnerPresent = true
		return state, nil
	}
	instance := peer.instance
	c.mu.Unlock()
	owner, err := c.verifyOwner(ctx, sessionID)
	if err != nil {
		if ctx.Err() != nil {
			return state, ctx.Err()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if current := c.peers[sessionID]; current != nil && current.instance == instance {
			current.verifiedAt = time.Time{}
			c.markUncertain(current)
			state.RecentInputs = c.outcomes(current)
		}
		state.UnavailableReason = "The original Claude owner could not be verified. No new input was sent."
		return state, agent.ErrNativeControlUnavailable
	}
	state.OwnerPresent = true
	c.mu.Lock()
	defer c.mu.Unlock()
	peer = c.peers[sessionID]
	if c.closed || peer == nil || peer.instance != instance || peer.owner.identity != owner.identity {
		return state, agent.ErrNativeControlStale
	}
	now := c.clock.Now()
	c.pruneCommands(now)
	peer.owner, peer.verifiedAt = owner, now
	state.RecentInputs = c.outcomes(peer)
	if !c.peerFresh(peer, now) {
		c.markUncertain(peer)
		state.RecentInputs = c.outcomes(peer)
		state.UnavailableReason = "The Claude plugin is disconnected; reconnect it in the original chat."
		return state, nil
	}
	state.ObservedAt = now
	state.ExpiresAt = now.Add(time.Duration(contracts.NativeSessionControlLeaseSeconds) * time.Second)
	state.BindingToken = c.binding(peer, state.ExpiresAt)
	state.ActivityState = owner.activity
	state.ActiveTurnID = peer.activeTurn
	state.CanSend = !peer.stopped
	state.CanInterrupt = peer.activeTurn != ""
	state.UnavailableReason = ""
	if peer.stopped {
		state.UnavailableReason = "Claude was stopped locally. Send a message in its original chat before remote input can continue."
	}
	return state, nil
}

func (c *nativeControlController) outcomes(peer *nativeControlPeer) []agent.NativeSessionInputOutcome {
	return append([]agent.NativeSessionInputOutcome(nil), peer.recent...)
}

func (c *nativeControlController) rememberOutcome(peer *nativeControlPeer, command *nativeControlCommand, fresh bool) {
	value := agent.NativeSessionInputOutcome{RequestID: command.RequestID, State: command.state,
		Detail: command.detail, TurnID: command.TurnID}
	if !fresh {
		for index := len(peer.recent) - 1; index >= 0; index-- {
			if peer.recent[index].RequestID == command.RequestID {
				peer.recent[index] = value
				return
			}
		}
		return
	}
	// A caller may reuse an ID only with a new valid lease after its confirmed
	// dedup record retires. Keep the recent projection unique as well.
	for index := len(peer.recent) - 1; index >= 0; index-- {
		if peer.recent[index].RequestID == command.RequestID {
			copy(peer.recent[index:], peer.recent[index+1:])
			clear(peer.recent[len(peer.recent)-1:])
			peer.recent = peer.recent[:len(peer.recent)-1]
		}
	}
	peer.recent = append(peer.recent, value)
	if len(peer.recent) > 20 {
		peer.recent = append([]agent.NativeSessionInputOutcome(nil), peer.recent[len(peer.recent)-20:]...)
	}
}

func nativeControlConfirmed(state leapmuxv1.NativeSessionInputState) bool {
	return state == leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED ||
		state == leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DROPPED ||
		state == leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED
}

// Every original browser lease has expired before a confirmed record retires.
// Unknown and in-flight delivery is never forgotten to make capacity available.
func (c *nativeControlController) pruneCommands(now time.Time) {
	retention := 2 * time.Duration(contracts.NativeSessionControlLeaseSeconds) * time.Second
	for _, peer := range c.peers {
		kept := peer.commands[:0]
		for _, command := range peer.commands {
			if nativeControlConfirmed(command.state) && !command.settledAt.IsZero() && !now.Before(command.settledAt.Add(retention)) {
				delete(peer.byRequest, command.RequestID)
				c.commandCount--
				continue
			}
			kept = append(kept, command)
		}
		clear(peer.commands[len(kept):])
		peer.commands = kept
	}
}

func (c *nativeControlController) prunePeerSlots(now time.Time, registering string) {
	c.pruneCommands(now)
	if len(c.peers) < nativeControlMaxPeers {
		return
	}
	retention := 2 * time.Duration(contracts.NativeSessionControlLeaseSeconds) * time.Second
	for id, peer := range c.peers {
		if id != registering && len(peer.commands) == 0 && !now.Before(peer.lastSeen.Add(retention)) {
			delete(c.peers, id)
		}
	}
}

func (c *nativeControlController) markUncertain(peer *nativeControlPeer) {
	for _, command := range peer.commands {
		if command.state == leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED {
			command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN
			command.detail = "Claude disconnected before confirming delivery. Do not resend automatically."
			c.finishCommand(command)
		}
	}
}

func (c *nativeControlController) Send(ctx context.Context, input agent.NativeSessionInput) (agent.NativeSessionInputReceipt, error) {
	if input.Behavior != leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_QUEUE_NEXT_TURN ||
		input.Text == "" || !utf8.ValidString(input.Text) || len(input.Text) > contracts.NativeSessionControlMaxTextBytes ||
		strings.IndexByte(input.Text, 0) >= 0 {
		return agent.NativeSessionInputReceipt{}, agent.ErrNativeControlInvalid
	}
	_, err := c.enqueue(ctx, input.SessionID, input.BindingToken, &nativeControlCommand{
		RequestID: input.RequestID, Kind: "send", Text: input.Text})
	if err != nil {
		return agent.NativeSessionInputReceipt{}, err
	}
	return agent.NativeSessionInputReceipt{Disposition: leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_QUEUED,
		Detail: "Queued for the original Claude chat. Delivery status updates below."}, nil
}

func (c *nativeControlController) Interrupt(ctx context.Context, input agent.NativeSessionInterrupt) error {
	if !validNativeSessionID(input.ExpectedTurnID) {
		return agent.ErrNativeControlInvalid
	}
	command, err := c.enqueue(ctx, input.SessionID, input.BindingToken, &nativeControlCommand{
		RequestID: input.RequestID, Kind: "interrupt", TurnID: input.ExpectedTurnID})
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return agent.ErrNativeControlUncertain
	case <-command.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		if command.state == leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED {
			return nil
		}
		if command.state == leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN {
			return agent.ErrNativeControlUncertain
		}
		return agent.ErrNativeControlStale
	}
}

func (c *nativeControlController) enqueue(ctx context.Context, sessionID, binding string, command *nativeControlCommand) (*nativeControlCommand, error) {
	if !validNativeSessionID(command.RequestID) {
		return nil, agent.ErrNativeControlInvalid
	}
	state, err := c.Inspect(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.pruneCommands(c.clock.Now())
	peer := c.peers[sessionID]
	if c.closed || peer == nil || !c.validBinding(peer, binding, c.clock.Now()) || !c.peerFresh(peer, c.clock.Now()) {
		return nil, agent.ErrNativeControlStale
	}
	encoded, _ := json.Marshal([]string{command.Kind, command.Text, command.TurnID, binding})
	command.fingerprint = sha256.Sum256(encoded)
	if previous := peer.byRequest[command.RequestID]; previous != nil {
		if previous.fingerprint != command.fingerprint {
			return nil, agent.ErrNativeControlInvalid
		}
		return previous, nil
	}
	if c.commandCount >= nativeControlMaxCommands {
		return nil, agent.ErrNativeControlUnavailable
	}
	if command.Kind == "send" && !state.CanSend {
		return nil, agent.ErrNativeControlUnavailable
	}
	if command.Kind == "interrupt" && (!state.CanInterrupt || peer.activeTurn != command.TurnID) {
		return nil, agent.ErrNativeControlStale
	}
	deliveryID, err := nativeControlRandom()
	if err != nil {
		return nil, agent.ErrNativeControlUnavailable
	}
	command.DeliveryID = deliveryID
	command.InstanceID, command.state = peer.instance, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED
	command.done = make(chan struct{})
	command.peer = peer
	peer.commands = append(peer.commands, command)
	peer.byRequest[command.RequestID] = command
	c.commandCount++
	c.rememberOutcome(peer, command, true)
	return command, nil
}

func (c *nativeControlController) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	for _, peer := range c.peers {
		for _, command := range peer.commands {
			if !command.finished {
				command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN
				c.finishCommand(command)
			}
		}
	}
	server, listener, lock := c.server, c.listener, c.lock
	c.mu.Unlock()
	var err error
	if server != nil {
		err = server.Close()
	} else if listener != nil {
		err = listener.Close()
	}
	if lock != nil {
		err = errors.Join(err, lock.Close())
	}
	return err
}

func (c *nativeControlController) finishCommand(command *nativeControlCommand) {
	if nativeControlConfirmed(command.state) && command.settledAt.IsZero() {
		command.settledAt = c.clock.Now()
	}
	if command.peer != nil {
		c.rememberOutcome(command.peer, command, false)
	}
	if !command.finished {
		command.Text = ""
		command.finished = true
		close(command.done)
	}
}
