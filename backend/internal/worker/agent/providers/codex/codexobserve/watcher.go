package codexobserve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"slices"
	"sort"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	maxSubscriptions = 8
	maxDiscoveries   = 32
	freshFor         = 45 * time.Second
	demandFor        = 45 * time.Second
	discoveryEvery   = 15 * time.Second
	heartbeatEvery   = 10 * time.Second
	requestFor       = 1500 * time.Millisecond
	snapshotFor      = 10 * time.Second
)

type dependencies struct {
	now        func() time.Time
	tick       func() (<-chan time.Time, func())
	paths      func() (string, string, error)
	connect    func(context.Context, string) (net.Conn, PeerIdentity, error)
	verify     func(context.Context, net.Conn, PeerIdentity) error
	candidates func(context.Context, string) (candidateSet, error)
}

// Watcher has one lazy connection and at most eight passive subscriptions.
// Closing the dashboard stops demand; it releases subscriptions after 45 seconds.
// Only status metadata reaches the cache; decoder token buffers are transient.
type Watcher struct {
	mu         sync.Mutex
	deps       dependencies
	lastDemand time.Time
	running    bool
	closed     bool
	cancel     context.CancelFunc
	done       chan struct{}
	cached     []agent.NativeServiceObservation
}

// NewWatcher creates no connection and reads no files until Inspect is requested.
func NewWatcher() *Watcher {
	return newWatcher(dependencies{
		now:   time.Now,
		tick:  func() (<-chan time.Time, func()) { t := time.NewTicker(time.Second); return t.C, t.Stop },
		paths: metadataPaths, connect: Connect, verify: VerifyPeer, candidates: readCandidates,
	})
}
func newWatcher(deps dependencies) *Watcher { return &Watcher{deps: deps} }

func (w *Watcher) Inspect(ctx context.Context) []agent.NativeServiceObservation {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || ctx.Err() != nil {
		return nil
	}
	now := w.deps.now()
	w.lastDemand = now
	if !w.running {
		ctx, cancel := context.WithCancel(context.Background())
		w.cancel = cancel
		w.done = make(chan struct{})
		w.running = true
		go w.run(ctx, w.done)
	}
	return copyObservations(w.cached, now)
}
func (w *Watcher) Close() error {
	w.mu.Lock()
	w.closed = true
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return nil
}
func copyObservations(input []agent.NativeServiceObservation, now time.Time) []agent.NativeServiceObservation {
	result := slices.Clone(input)
	for i := range result {
		result[i].Sessions = slices.Clone(result[i].Sessions)
		for j := range result[i].Sessions {
			s := &result[i].Sessions[j]
			if !result[i].Available || s.ExpiresAt.IsZero() || !now.Before(s.ExpiresAt) {
				s.ActivityState = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
			}
		}
	}
	return result
}
func (w *Watcher) demandExpired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.deps.now().Before(w.lastDemand.Add(demandFor))
}
func (w *Watcher) publish(s *watchState) {
	w.mu.Lock()
	w.cached = []agent.NativeServiceObservation{s.observation(w.deps.now())}
	w.mu.Unlock()
}
func (w *Watcher) stopped(done chan struct{}) {
	w.mu.Lock()
	for i := range w.cached {
		w.cached[i].Available = false
		for j := range w.cached[i].Sessions {
			w.cached[i].Sessions[j].ActivityState = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
			w.cached[i].Sessions[j].ExpiresAt = w.deps.now()
		}
	}
	w.running = false
	close(done)
	w.mu.Unlock()
}

type followed struct {
	candidate             candidate
	owner                 string
	state                 projection
	revision              int64
	snapshotRevisionFloor int64
	valid                 bool
	statusAt              time.Time
	verifiedAt            time.Time
	expiresAt             time.Time
	snapshotDeadline      time.Time
	lastSnapshotRequest   time.Time
}
type pendingRequest struct {
	method    string
	candidate candidate
	owner     string
	deadline  time.Time
}
type watchState struct {
	peer          PeerIdentity
	sourceID      string
	clientID      string
	followed      map[string]*followed
	pending       map[string]pendingRequest
	candidates    candidateSet
	candidatesOK  bool
	cursor        int
	lastDiscovery time.Time
	lastHeartbeat time.Time
	initialized   bool
}

func (s *watchState) observation(now time.Time) agent.NativeServiceObservation {
	result := agent.NativeServiceObservation{
		SourceID: s.sourceID, ServicePID: s.peer.PID, ProcessStartTime: s.peer.ProcessStartTime,
		ExecutableName: s.peer.ExecutableName, Available: s.initialized && s.candidatesOK,
		// There is no supported enumeration of owners. Even a complete candidate scan
		// cannot prove coverage of every active chat (e.g. a custom metadata root).
		Partial: true, CandidateCount: len(s.candidates.Candidates), CandidateTruncated: s.candidates.Truncated,
		SubscriptionLimit: maxSubscriptions,
	}
	for _, f := range s.followed {
		status := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
		if f.valid && now.Before(f.expiresAt) {
			switch f.state.status() {
			case "working":
				status = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
			case "waiting":
				status = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
			case "idle":
				status = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE
			}
		}
		result.Sessions = append(result.Sessions, agent.NativeServiceSessionObservation{
			SessionID: f.candidate.ID, DisplayName: f.candidate.Name, ActivityState: status,
			StatusObservedAt: f.statusAt, SourceVerifiedAt: f.verifiedAt, ExpiresAt: f.expiresAt,
		})
	}
	sort.Slice(result.Sessions, func(i, j int) bool { return result.Sessions[i].SessionID < result.Sessions[j].SessionID })
	return result
}
func (f *followed) invalidate(now time.Time) { f.valid = false; f.expiresAt = now }
func (f *followed) verifyOwner(owner string, now time.Time) error {
	if f.owner != owner {
		f.invalidate(now)
		return errProtocol
	}
	if f.valid {
		f.verifiedAt = now
		f.expiresAt = now.Add(freshFor)
	}
	return nil
}

// acceptState mutates only after the complete JSON frame has been validated.
// Every relevant owner/version/thread mismatch fails closed. Content-only patches
// advance revision but do not invent new status-observation timestamps.
func (s *watchState) acceptState(frame frame, now time.Time) error {
	f := s.followed[frame.ConversationID]
	if f == nil {
		return nil
	}
	if frame.Version != 11 || frame.SourceID != f.owner || frame.HostID != "local" || !slices.Contains(frame.Targets, s.clientID) {
		f.invalidate(now)
		return errProtocol
	}
	c := frame.Change
	switch c.Kind {
	case "snapshot":
		if c.Revision < 0 || c.State.ID != f.candidate.ID {
			f.invalidate(now)
			return errProtocol
		}
		if c.Revision < max(f.revision, f.snapshotRevisionFloor) {
			f.invalidate(now)
			return errProtocol
		}
		f.snapshotRevisionFloor = c.Revision
		f.state = c.State
		f.revision = c.Revision
		f.valid = true
		f.statusAt = now
		f.verifiedAt = now
		f.expiresAt = now.Add(freshFor)
		f.snapshotDeadline = time.Time{}
	case "patches":
		// A structurally valid update from this owner proves that its revision
		// exists even when a gap or unsupported patch makes our state unusable.
		if c.BaseRevision >= 0 && c.Revision == c.BaseRevision+1 {
			f.snapshotRevisionFloor = max(f.snapshotRevisionFloor, c.Revision)
		}
		if !f.valid || c.BaseRevision != f.revision || c.Revision != c.BaseRevision+1 {
			f.invalidate(now)
			return errProtocol
		}
		next, changed, err := applyPatches(f.state, c.Patches)
		if err != nil || next.ID != f.candidate.ID {
			f.invalidate(now)
			return errProtocol
		}
		f.state = next
		f.revision = c.Revision
		if changed {
			f.statusAt = now
		}
		// An authenticated, contiguous patch proves stream continuity even when it
		// only changes content that was skipped by the parser.
		f.verifiedAt = now
		f.expiresAt = now.Add(freshFor)
	default:
		f.invalidate(now)
		return errProtocol
	}
	return nil
}

type readResult struct {
	frame frame
	err   error
}

func (w *Watcher) run(ctx context.Context, done chan struct{}) {
	defer w.stopped(done)
	home, path, err := w.deps.paths()
	if err != nil {
		return
	}
	setup, cancelSetup := context.WithTimeout(ctx, snapshotFor)
	conn, peer, err := w.deps.connect(setup, home)
	cancelSetup()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	state := &watchState{
		peer: peer, sourceID: peer.SourceID + "-" + newID(),
		followed: map[string]*followed{}, pending: map[string]pendingRequest{},
	}
	reads := make(chan readResult, 1)
	readerDone := make(chan struct{})
	readCtx, cancelReader := context.WithCancel(ctx)
	defer func() { cancelReader(); _ = conn.Close(); <-readerDone }()
	defer func() {
		if conn.SetWriteDeadline(time.Now().Add(500*time.Millisecond)) != nil {
			return
		}
		for id, f := range state.followed {
			_ = writeFrame(conn, followingMessage(state.clientID, f.owner, id, false))
		}
	}()
	go func() {
		defer close(readerDone)
		for {
			var f frame
			err := conn.SetReadDeadline(time.Now().Add(freshFor))
			if err == nil {
				f, err = readFrame(conn)
			}
			select {
			case reads <- readResult{f, err}:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ticks, stopTicks := w.deps.tick()
	defer stopTicks()
	requestID := newID()
	state.pending[requestID] = pendingRequest{method: "initialize", deadline: w.deps.now().Add(requestFor)}
	if sendRequest(conn, requestID, "initializing-client", "initialize", 0, map[string]any{"clientType": "leapmux-observer"}) != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case result := <-reads:
			if result.err != nil {
				return
			}
			f := result.frame
			now := w.deps.now()
			switch f.Type {
			case "client-discovery-request":
				if !uuidPattern.MatchString(f.RequestID) {
					return
				}
				if sendMetadata(conn, map[string]any{"type": "client-discovery-response", "requestId": f.RequestID, "response": map[string]any{"canHandle": false}}) != nil {
					return
				}
			case "response":
				pending, ok := state.pending[f.RequestID]
				if !ok {
					continue
				}
				delete(state.pending, f.RequestID)
				if f.Method != pending.method || !now.Before(pending.deadline) {
					return
				}
				if pending.method == "initialize" {
					if f.ResultType != "success" || !uuidPattern.MatchString(f.ClientID) || f.HandledBy != f.ClientID {
						return
					}
					if w.deps.verify(ctx, conn, peer) != nil {
						return
					}
					state.clientID = f.ClientID
					state.initialized = true
					if !w.refresh(ctx, conn, state, path) {
						return
					}
				} else if f.ResultType == "success" && uuidPattern.MatchString(f.HandledBy) && f.HandledBy != state.clientID {
					if pending.owner != "" && pending.owner != f.HandledBy {
						return
					}
					if w.deps.verify(ctx, conn, peer) != nil {
						return
					}
					existing := state.followed[pending.candidate.ID]
					if existing != nil {
						if existing.verifyOwner(f.HandledBy, now) != nil {
							return
						}
					} else if len(state.followed) < maxSubscriptions {
						state.followed[pending.candidate.ID] = &followed{
							candidate: pending.candidate, owner: f.HandledBy, revision: -1,
							snapshotDeadline: now.Add(snapshotFor), lastSnapshotRequest: now,
						}
						if sendFollowing(conn, state.clientID, f.HandledBy, pending.candidate.ID, true) != nil {
							return
						}
					}
				} else if existing := state.followed[pending.candidate.ID]; existing != nil {
					existing.invalidate(now)
					// A lost owner must not remain loaded merely because the dashboard exists.
					_ = sendFollowing(conn, state.clientID, existing.owner, pending.candidate.ID, false)
					delete(state.followed, pending.candidate.ID)
				}
			case "broadcast":
				switch f.Method {
				case "ipc-connection-reset":
					return
				case "client-status-changed":
					if f.Version != 0 {
						return
					}
					if f.Status == "disconnected" {
						for _, follow := range state.followed {
							if follow.owner == f.ClientID {
								return
							}
						}
					}
				case "thread-stream-state-changed":
					// A gap requires an explicit new snapshot, never a guessed current state.
					if state.acceptState(f, now) != nil {
						follow := state.followed[f.ConversationID]
						if follow != nil && now.Sub(follow.lastSnapshotRequest) >= discoveryEvery {
							follow.lastSnapshotRequest = now
							follow.snapshotDeadline = now.Add(snapshotFor)
							if sendFollowing(conn, state.clientID, follow.owner, f.ConversationID, true) != nil {
								return
							}
						}
					}
				case "thread-stream-following-status-requested":
					if f.Version != 1 || f.HostID != "local" {
						return
					}
					follow := state.followed[f.ConversationID]
					if follow != nil && f.SourceID == follow.owner {
						if sendFollowing(conn, state.clientID, follow.owner, f.ConversationID, true) != nil {
							return
						}
					}
				}
			}
			w.publish(state)
		case <-ticks:
			now := w.deps.now()
			if w.demandExpired() {
				return
			}
			for id, pending := range state.pending {
				if now.Before(pending.deadline) {
					continue
				}
				delete(state.pending, id)
				if pending.method == "initialize" {
					return
				}
				if follow := state.followed[pending.candidate.ID]; follow != nil {
					follow.invalidate(now)
				}
			}
			for id, follow := range state.followed {
				if !follow.valid && follow.snapshotDeadline.IsZero() && now.Sub(follow.lastSnapshotRequest) >= discoveryEvery {
					follow.lastSnapshotRequest = now
					follow.snapshotDeadline = now.Add(snapshotFor)
					if sendFollowing(conn, state.clientID, follow.owner, id, true) != nil {
						return
					}
				}
				if !follow.snapshotDeadline.IsZero() && !now.Before(follow.snapshotDeadline) {
					follow.invalidate(now)
					_ = sendFollowing(conn, state.clientID, follow.owner, id, false)
					delete(state.followed, id)
				}
			}
			if !state.initialized {
				continue
			}
			if now.Sub(state.lastHeartbeat) >= heartbeatEvery {
				if w.deps.verify(ctx, conn, peer) != nil {
					return
				}
				state.lastHeartbeat = now
				for _, follow := range state.followed {
					if !w.discover(conn, state, follow.candidate, follow.owner) {
						return
					}
				}
			}
			if now.Sub(state.lastDiscovery) >= discoveryEvery {
				if !w.refresh(ctx, conn, state, path) {
					return
				}
			}
			w.publish(state)
		}
	}
}

func (w *Watcher) refresh(ctx context.Context, conn net.Conn, state *watchState, path string) bool {
	state.lastDiscovery = w.deps.now()
	candidates, err := w.deps.candidates(ctx, path)
	if err != nil {
		state.candidatesOK = false
		return true
	}
	state.candidatesOK = true
	state.candidates = candidates
	names := map[string]string{}
	for _, c := range candidates.Candidates {
		names[c.ID] = c.Name
	}
	for _, f := range state.followed {
		if name, ok := names[f.candidate.ID]; ok {
			f.candidate.Name = name
		}
	}
	if len(candidates.Candidates) == 0 {
		return true
	}
	for count := 0; count < maxDiscoveries && count < len(candidates.Candidates); count++ {
		index := state.cursor % len(candidates.Candidates)
		state.cursor++
		c := candidates.Candidates[index]
		if state.followed[c.ID] != nil {
			continue
		}
		if !w.discover(conn, state, c, "") {
			return false
		}
	}
	return true
}
func (w *Watcher) discover(conn net.Conn, state *watchState, candidate candidate, owner string) bool {
	for _, pending := range state.pending {
		if pending.candidate.ID == candidate.ID {
			return true
		}
	}
	id := newID()
	state.pending[id] = pendingRequest{method: "thread-owner-discovery", candidate: candidate, owner: owner, deadline: w.deps.now().Add(requestFor)}
	return sendRequest(conn, id, state.clientID, "thread-owner-discovery", 1, map[string]any{"hostId": "local", "conversationId": candidate.ID}) == nil
}

func sendRequest(conn net.Conn, id, client, method string, version int, params map[string]any) error {
	return sendMetadata(conn, map[string]any{
		"type": "request", "requestId": id, "sourceClientId": client, "method": method, "version": version,
		"params": params, "timeoutMs": 1500,
	})
}
func sendFollowing(conn net.Conn, client, owner, id string, following bool) error {
	return sendMetadata(conn, followingMessage(client, owner, id, following))
}
func followingMessage(client, owner, id string, following bool) map[string]any {
	return map[string]any{
		"type": "broadcast", "method": "thread-stream-following-changed", "sourceClientId": client,
		"targetClientIds": []string{owner}, "version": 1,
		"params": map[string]any{"hostId": "local", "conversationId": id, "following": following},
	}
}
func sendMetadata(conn net.Conn, value any) error {
	if conn.SetWriteDeadline(time.Now().Add(requestFor)) != nil {
		return errProtocol
	}
	return writeFrame(conn, value)
}
func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic("native status random source unavailable")
	}
	bytes[6] = (bytes[6] & 15) | 64
	bytes[8] = (bytes[8] & 63) | 128
	encoded := hex.EncodeToString(bytes[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}
