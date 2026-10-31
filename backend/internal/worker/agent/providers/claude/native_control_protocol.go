package claude

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type nativeControlFrame struct {
	SessionID  string                 `json:"sessionId"`
	InstanceID string                 `json:"instanceId"`
	Version    string                 `json:"version,omitempty"`
	ActiveTurn string                 `json:"activeTurn,omitempty"`
	Stopped    bool                   `json:"stopped"`
	Outcomes   []nativeControlOutcome `json:"outcomes,omitempty"`
}

type nativeControlOutcome struct {
	RequestID  string `json:"requestId"`
	DeliveryID string `json:"deliveryId"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	TurnID     string `json:"turnId,omitempty"`
}

func (c *nativeControlController) handle(w http.ResponseWriter, r *http.Request) {
	// There is no browser/CORS endpoint. Every request is authenticated with a
	// secret held in an owner-only local descriptor, never a URL parameter.
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(ip).IsLoopback() || r.Method != http.MethodPost ||
		r.Header.Get("Origin") != "" || r.Header.Get("Content-Type") != "application/json" ||
		subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+c.token)) != 1 {
		http.Error(w, "unavailable", http.StatusForbidden)
		return
	}
	var frame nativeControlFrame
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, nativeControlMaxBody))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&frame) != nil || decoder.Decode(new(any)) != io.EOF ||
		!validNativeSessionID(frame.SessionID) || !validNativeSessionID(frame.InstanceID) ||
		(frame.ActiveTurn != "" && !validNativeSessionID(frame.ActiveTurn)) || len(frame.Outcomes) > 64 {
		http.Error(w, "invalid", http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/register" {
		c.registerPeer(w, r, frame)
		return
	}
	if r.URL.Path != "/poll" {
		http.NotFound(w, r)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	peer := c.peers[frame.SessionID]
	if c.closed || peer == nil || peer.instance != frame.InstanceID {
		http.Error(w, "stale", http.StatusConflict)
		return
	}
	now := c.clock.Now()
	peer.lastSeen, peer.activeTurn, peer.stopped = now, frame.ActiveTurn, frame.Stopped
	for _, outcome := range frame.Outcomes {
		c.applyOutcome(peer, outcome)
	}
	commands := make([]nativeControlCommand, 0)
	if c.peerFresh(peer, now) {
		for _, command := range peer.commands {
			// Mods buffers HTTP responses up to 4 MiB. Even worst-case JSON
			// escaping of eight maximum-size inputs remains below that limit.
			if len(commands) >= 8 {
				break
			}
			if command.state != leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED {
				continue
			}
			if command.InstanceID != peer.instance {
				continue
			}
			if command.Kind == "interrupt" && command.TurnID != peer.activeTurn {
				command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED
				command.detail = "The Claude turn changed before interruption."
				c.finishCommand(command)
				continue
			}
			if command.Kind == "send" && peer.stopped {
				command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DROPPED
				command.detail = "Claude requires input in its original chat after a local Stop."
				c.finishCommand(command)
				continue
			}
			// Dispatch once. Losing this HTTP response is uncertain delivery;
			// neither a reconnect nor another poll replays the prompt.
			command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED
			c.rememberOutcome(peer, command, false)
			commands = append(commands, *command)
		}
	}
	c.reply(w, map[string]any{"epoch": c.epoch, "commands": commands})
}

func (c *nativeControlController) registerPeer(w http.ResponseWriter, r *http.Request, frame nativeControlFrame) {
	if !nativeControlVersionAllowed(frame.Version) {
		http.Error(w, "unsupported", http.StatusConflict)
		return
	}
	owner, err := c.verifyOwner(r.Context(), frame.SessionID)
	if err != nil {
		http.Error(w, "owner_unavailable", http.StatusConflict)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	c.prunePeerSlots(c.clock.Now(), frame.SessionID)
	peer := c.peers[frame.SessionID]
	if peer != nil && (peer.owner.identity != owner.identity || peer.instance != frame.InstanceID) {
		for _, command := range peer.commands {
			switch command.state {
			case leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNSPECIFIED:
				command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN
				command.detail = "The original Claude bridge could not confirm this operation."
				c.finishCommand(command)
			case leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED:
				command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED
				command.detail = "Claude bridge reloaded before delivery."
				c.finishCommand(command)
			case leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED:
				command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN
				command.detail = "The original Claude bridge disconnected before confirming delivery. Do not resend automatically."
				c.finishCommand(command)
			case leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED,
				leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DROPPED,
				leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED,
				leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN:
				// Settled and uncertain outcomes stay queryable without replay.
			}
		}
	}
	if peer == nil {
		if len(c.peers) >= nativeControlMaxPeers {
			http.Error(w, "capacity", http.StatusServiceUnavailable)
			return
		}
		peer = &nativeControlPeer{sessionID: frame.SessionID, byRequest: make(map[string]*nativeControlCommand)}
		c.peers[frame.SessionID] = peer
	}
	peer.instance, peer.owner = frame.InstanceID, owner
	peer.lastSeen, peer.verifiedAt = c.clock.Now(), c.clock.Now()
	peer.activeTurn, peer.stopped = frame.ActiveTurn, frame.Stopped
	c.reply(w, map[string]any{"registered": true, "epoch": c.epoch})
}

func nativeControlVersionAllowed(version string) bool {
	// The supported Mods interface begins at 2.1.287. The actual disposable
	// integration proof ran on 2.1.308; reject ambiguous version strings.
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	var values [3]int
	for i, part := range parts {
		if part == "" || len(part) > 6 {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
			values[i] = values[i]*10 + int(char-'0')
		}
	}
	return values[0] > 2 || (values[0] == 2 && (values[1] > 1 || values[1] == 1 && values[2] >= 287))
}

func (c *nativeControlController) applyOutcome(peer *nativeControlPeer, outcome nativeControlOutcome) {
	command := peer.byRequest[outcome.RequestID]
	if command == nil || len(outcome.DeliveryID) != 43 || !validNativeSessionID(outcome.DeliveryID) ||
		outcome.DeliveryID != command.DeliveryID || command.InstanceID != peer.instance ||
		(command.state != leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED &&
			command.state != leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN) {
		return
	}
	switch outcome.State {
	case "started":
		if command.Kind != "send" {
			return
		}
		command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED
		command.detail = "Claude started this input in the original conversation."
		if validNativeSessionID(outcome.TurnID) {
			command.TurnID = outcome.TurnID
		}
	case "dropped":
		command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DROPPED
		command.detail = "Claude did not accept this input."
		if outcome.Reason == "local_stop" {
			peer.stopped = true
			command.detail = "Claude requires input in its original chat after a local Stop."
		}
	case "rejected", "stale_turn":
		command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED
		command.detail = "The original Claude owner refused this operation."
	case "interrupted":
		if command.Kind == "interrupt" && outcome.TurnID == command.TurnID {
			command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED
			command.detail = "The exact Claude turn was interrupted."
		} else {
			command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED
			command.detail = "The interruption acknowledgement did not match the requested Claude turn."
		}
	}
	if command.state != leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED {
		c.finishCommand(command)
	}
}

func (c *nativeControlController) reply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
