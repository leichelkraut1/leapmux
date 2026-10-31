package codexobserve

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// These are the installed Desktop/IDE owner-routing protocol versions, not
// app-server JSON-RPC. No request may start/resume an app-server or claim a chat.
const controlWireLimit = 64 << 20

// JSON can escape each permitted input byte as six bytes, plus envelope fields.
const controlOutboundLimit = 6*contracts.NativeSessionControlMaxTextBytes + 8192

var errControlNoOwner = errors.New("no native Codex owner is registered")

type controlFrame struct {
	Type       string          `json:"type"`
	Method     string          `json:"method"`
	RequestID  string          `json:"requestId"`
	SourceID   string          `json:"sourceClientId"`
	HandledBy  string          `json:"handledByClientId"`
	ResultType string          `json:"resultType"`
	Version    int             `json:"version"`
	Targets    []string        `json:"targetClientIds"`
	Error      string          `json:"error"`
	Result     json.RawMessage `json:"result"`
	Params     json.RawMessage `json:"params"`
}

type controlTurn struct {
	ID     string `json:"turnId"`
	Status string `json:"status"`
}

type controlSnapshot struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Resume    string `json:"resumeState"`
	Runtime   struct {
		Type  string   `json:"type"`
		Flags []string `json:"activeFlags"`
	} `json:"threadRuntimeStatus"`
	Turns       []controlTurn `json:"turns"`
	TurnHistory struct {
		Kind    string `json:"kind"`
		History struct {
			Entities map[string]controlTurn `json:"entitiesByKey"`
		} `json:"history"`
	} `json:"turnHistory"`
}

type controlConnection struct {
	conn             net.Conn
	peer             PeerIdentity
	verify           func(context.Context, net.Conn, PeerIdentity) error
	client           string
	owner            string
	thread           string
	stopCancellation func() bool
}

func (s *controlConnection) deadline(ctx context.Context) error {
	deadline := time.Now().Add(snapshotFor)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	return s.conn.SetDeadline(deadline)
}

func (s *controlConnection) write(ctx context.Context, value any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.verify(ctx, s.conn, s.peer) != nil {
		return agent.ErrNativeControlStale
	}
	if s.deadline(ctx) != nil {
		return agent.ErrNativeControlUnavailable
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > controlOutboundLimit {
		return agent.ErrNativeControlInvalid
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(raw)))
	if writeAll(s.conn, header[:]) != nil || writeAll(s.conn, raw) != nil {
		return agent.ErrNativeControlUncertain
	}
	return nil
}

func (s *controlConnection) read(ctx context.Context) (controlFrame, error) {
	if ctx.Err() != nil {
		return controlFrame{}, ctx.Err()
	}
	if s.deadline(ctx) != nil {
		return controlFrame{}, agent.ErrNativeControlUnavailable
	}
	var header [4]byte
	if _, err := io.ReadFull(s.conn, header[:]); err != nil {
		return controlFrame{}, agent.ErrNativeControlUncertain
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > controlWireLimit {
		return controlFrame{}, agent.ErrNativeControlUnavailable
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(s.conn, raw); err != nil {
		return controlFrame{}, agent.ErrNativeControlUncertain
	}
	var frame controlFrame
	if json.Unmarshal(raw, &frame) != nil {
		return controlFrame{}, agent.ErrNativeControlUnavailable
	}
	if s.verify(ctx, s.conn, s.peer) != nil {
		return controlFrame{}, agent.ErrNativeControlStale
	}
	return frame, nil
}

func (s *controlConnection) ignore(ctx context.Context, f controlFrame) error {
	if f.Type == "client-discovery-request" {
		return s.write(ctx, map[string]any{"type": "client-discovery-response", "requestId": f.RequestID, "response": map[string]bool{"canHandle": false}})
	}
	return nil
}

func (s *controlConnection) request(ctx context.Context, method string, version int, params any, target string) (json.RawMessage, error) {
	id := newID()
	request := map[string]any{"type": "request", "requestId": id, "sourceClientId": s.client, "method": method, "version": version, "params": params, "timeoutMs": 8000}
	if target != "" {
		request["targetClientId"] = target
	}
	if err := s.write(ctx, request); err != nil {
		return nil, err
	}
	// Once a mutation frame was written, a missing/unreadable reply cannot prove
	// that delivery failed. In particular a closed pipe can fail SetDeadline
	// before Read begins. Keep that outcome uncertain and never replay it.
	afterWriteError := func(err error) error {
		if method == "thread-follower-start-turn" || method == "thread-follower-interrupt-turn" {
			return agent.ErrNativeControlUncertain
		}
		return err
	}
	for count := 0; count < 256; count++ {
		f, err := s.read(ctx)
		if err != nil {
			return nil, afterWriteError(err)
		}
		if f.Type != "response" || f.RequestID != id {
			if err := s.ignore(ctx, f); err != nil {
				return nil, afterWriteError(err)
			}
			continue
		}
		if f.ResultType == "error" {
			if f.Error == "no-client-found" {
				if method == "thread-owner-discovery" {
					return nil, errControlNoOwner
				}
				return nil, agent.ErrNativeControlStale
			}
			return nil, agent.ErrNativeControlUncertain
		}
		if f.ResultType != "success" || f.Method != method || !uuidPattern.MatchString(f.HandledBy) || (target != "" && f.HandledBy != target) {
			return nil, agent.ErrNativeControlUncertain
		}
		if method == "initialize" {
			var result struct {
				ClientID string `json:"clientId"`
			}
			if json.Unmarshal(f.Result, &result) != nil || result.ClientID != f.HandledBy {
				return nil, agent.ErrNativeControlUnavailable
			}
			s.client = result.ClientID
		}
		if method == "thread-owner-discovery" {
			s.owner = f.HandledBy
		}
		return f.Result, nil
	}
	return nil, afterWriteError(agent.ErrNativeControlUnavailable)
}

func (s *controlConnection) initialize(ctx context.Context) error {
	s.client = "initializing-client"
	_, err := s.request(ctx, "initialize", 0, map[string]string{"clientType": "leapmux-controller"}, "")
	return err
}

func (s *controlConnection) snapshot(ctx context.Context) (controlSnapshot, error) {
	_, err := s.request(ctx, "thread-owner-discovery", 1, map[string]string{"hostId": "local", "conversationId": s.thread}, "")
	if err != nil {
		return controlSnapshot{}, err
	}
	if !uuidPattern.MatchString(s.owner) || s.owner == s.client {
		return controlSnapshot{}, agent.ErrNativeControlUnavailable
	}
	if err := s.write(ctx, followingMessage(s.client, s.owner, s.thread, true)); err != nil {
		return controlSnapshot{}, err
	}
	for count := 0; count < 256; count++ {
		f, err := s.read(ctx)
		if err != nil {
			return controlSnapshot{}, err
		}
		if f.Type != "broadcast" || f.Method != "thread-stream-state-changed" {
			if err := s.ignore(ctx, f); err != nil {
				return controlSnapshot{}, err
			}
			continue
		}
		var params struct {
			Host   string `json:"hostId"`
			Thread string `json:"conversationId"`
			Change struct {
				Type     string          `json:"type"`
				Revision *int64          `json:"revision"`
				State    controlSnapshot `json:"conversationState"`
			} `json:"change"`
		}
		if json.Unmarshal(f.Params, &params) != nil {
			return controlSnapshot{}, agent.ErrNativeControlUnavailable
		}
		if params.Thread != s.thread {
			continue
		}
		if f.Version != 11 || f.SourceID != s.owner || !slices.Contains(f.Targets, s.client) || params.Host != "local" {
			return controlSnapshot{}, agent.ErrNativeControlStale
		}
		if params.Change.Type != "snapshot" {
			continue
		}
		if params.Change.Revision == nil || *params.Change.Revision < 0 || params.Change.State.ID != s.thread || params.Change.State.SessionID != s.thread || params.Change.State.Resume != "resumed" {
			return controlSnapshot{}, agent.ErrNativeControlUnavailable
		}
		return params.Change.State, nil
	}
	return controlSnapshot{}, agent.ErrNativeControlUnavailable
}

func (s *controlConnection) close() {
	if s.stopCancellation != nil {
		s.stopCancellation()
	}
	if s.owner != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = s.write(ctx, followingMessage(s.client, s.owner, s.thread, false))
		cancel()
	}
	_ = s.conn.Close()
}

func (s controlSnapshot) activeTurn() string {
	active := ""
	consider := func(turn controlTurn) bool {
		if turn.Status != "inProgress" || turn.ID == "" {
			return true
		}
		if !uuidPattern.MatchString(turn.ID) || (active != "" && active != turn.ID) {
			return false
		}
		active = turn.ID
		return true
	}
	if s.TurnHistory.Kind == "canonical" {
		for _, turn := range s.TurnHistory.History.Entities {
			if !consider(turn) {
				return ""
			}
		}
	} else {
		for _, turn := range s.Turns {
			if !consider(turn) {
				return ""
			}
		}
	}
	return active
}
