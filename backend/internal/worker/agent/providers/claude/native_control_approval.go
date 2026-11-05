package claude

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const nativeControlApprovalPoll = 5 * time.Second

// A fresh ID names one invocation of the real PermissionRequest hook. The
// public token also binds its complete immutable content and owner generation.
// No tool.check verdict, permission rule, or stored transcript creates a prompt.
type nativeControlApprovalFrame struct {
	Protocol int    `json:"protocol"`
	ID       string `json:"id"`
	TurnID   string `json:"turnId"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

type nativeControlPendingApproval struct {
	rawID     string
	turnID    string
	title     string
	detail    string
	deadline  time.Time
	wake      chan struct{}
	command   *nativeControlCommand
	connected bool
}

var _ agent.NativeSessionApprovalController = (*nativeControlController)(nil)

func validNativeApprovalText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func (c *nativeControlController) approvalToken(peer *nativeControlPeer, frame *nativeControlApprovalFrame) string {
	encoded, _ := json.Marshal([]any{c.epoch, peer.instance, peer.sessionID, peer.owner.identity, frame})
	mac := hmac.New(sha256.New, []byte(c.token))
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c *nativeControlController) pendingApprovals(peer *nativeControlPeer, now time.Time) []agent.NativeSessionApproval {
	result := make([]agent.NativeSessionApproval, 0, len(peer.approvals))
	for id, pending := range peer.approvals {
		if !now.Before(pending.deadline) || pending.turnID != peer.activeTurn || peer.stopped {
			continue
		}
		value := agent.NativeSessionApproval{PromptID: id, TurnID: pending.turnID,
			Title: pending.title, Detail: pending.detail,
			Options: []agent.NativeSessionApprovalOption{
				{ID: "allow_once", Label: "Allow once", Description: "Allow this exact tool request once. No permission rules are saved."},
				{ID: "deny", Label: "Deny", Description: "Refuse this tool request."},
				{ID: "original_app", Label: "Use original app", Description: "Continue the original permission prompt without granting or denying it here."},
			}, CanAnswer: pending.command == nil && c.peerFresh(peer, now)}
		if pending.command != nil {
			value.UnavailableReason = "An answer is already being delivered. Do not submit it again."
		}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PromptID < result[j].PromptID })
	return result
}

// The background poll reports the invocations still held by this plugin. A
// locally resolved/cancelled request disappears even if its HTTP call is still
// waiting. The allow/deny result is never retained for another invocation.
func (c *nativeControlController) cancelApprovals(peer *nativeControlPeer, keeping []string) {
	for id, pending := range peer.approvals {
		keep := false
		for _, rawID := range keeping {
			if rawID == pending.rawID {
				keep = true
				break
			}
		}
		if keep && pending.turnID == peer.activeTurn && !peer.stopped && c.clock.Now().Before(pending.deadline) {
			continue
		}
		delete(peer.approvals, id)
		if pending.command == nil {
			close(pending.wake)
		} else if pending.command.state == leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED {
			pending.command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED
			pending.command.detail = "The Claude permission request was resolved or cancelled before delivery."
			c.finishCommand(pending.command)
		}
	}
}

func (c *nativeControlController) waitApproval(w http.ResponseWriter, r *http.Request, frame nativeControlFrame) {
	value := frame.Approval
	if value == nil || value.Protocol != 1 || !validNativeSessionID(value.ID) ||
		!validNativeSessionID(value.TurnID) || frame.ActiveTurn != value.TurnID || frame.Stopped ||
		!validNativeApprovalText(value.Title, contracts.NativeSessionControlMaxApprovalTextBytes) ||
		!validNativeApprovalText(value.Detail, contracts.NativeSessionControlMaxApprovalDetailBytes) {
		http.Error(w, "invalid", http.StatusBadRequest)
		return
	}
	owner, err := c.verifyOwner(r.Context(), frame.SessionID)
	if err != nil {
		http.Error(w, "owner_unavailable", http.StatusConflict)
		return
	}
	c.mu.Lock()
	peer := c.peers[frame.SessionID]
	if c.closed || peer == nil || peer.instance != frame.InstanceID || peer.owner.identity != owner.identity ||
		peer.activeTurn != value.TurnID || peer.stopped {
		c.mu.Unlock()
		http.Error(w, "stale", http.StatusConflict)
		return
	}
	// This is a new, successful native owner verification, not a timer renewing
	// an inferred activity record. It keeps a held request usable when no
	// browser is open. The independent plugin heartbeat must also stay fresh.
	peer.owner, peer.verifiedAt = owner, c.clock.Now()
	if !c.peerFresh(peer, c.clock.Now()) {
		c.mu.Unlock()
		http.Error(w, "stale", http.StatusConflict)
		return
	}
	if peer.approvals == nil {
		peer.approvals = make(map[string]*nativeControlPendingApproval)
	}
	id := c.approvalToken(peer, value)
	pending := peer.approvals[id]
	for _, existing := range peer.approvals {
		if existing.rawID == value.ID && (existing != pending || existing.connected ||
			existing.command != nil && existing.command.state != leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED ||
			!c.clock.Now().Before(existing.deadline)) {
			c.mu.Unlock()
			http.Error(w, "duplicate", http.StatusConflict)
			return
		}
	}
	if pending == nil {
		if len(peer.approvals) >= contracts.NativeSessionControlMaxPendingApprovals {
			c.mu.Unlock()
			http.Error(w, "capacity", http.StatusConflict)
			return
		}
		pending = &nativeControlPendingApproval{rawID: value.ID, turnID: value.TurnID,
			title: value.Title, detail: value.Detail, wake: make(chan struct{})}
		peer.approvals[id] = pending
	}
	pending.connected = true
	pending.deadline = c.clock.Now().Add(time.Duration(contracts.NativeSessionControlLeaseSeconds) * time.Second)
	c.mu.Unlock()
	keep := false
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if peer.approvals[id] == pending {
			pending.connected = false
			if keep && r.Context().Err() == nil && pending.command == nil {
				return
			}
			delete(peer.approvals, id)
			if pending.command != nil && pending.command.state == leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED {
				pending.command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED
				pending.command.detail = "The Claude permission request closed before delivery."
				c.finishCommand(pending.command)
			}
		}
	}()
	// Only this endpoint needs a human response window. Every other endpoint
	// retains the server's short read/write deadlines.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(nativeControlApprovalPoll + 5*time.Second)); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	timer := c.clock.NewTimer(nativeControlApprovalPoll, "claude-native-approval-wait")
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-timer.C:
		keep = true
		c.reply(w, map[string]any{"epoch": c.epoch, "waiting": true})
		return
	case <-pending.wake:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	command := pending.command
	if c.closed || peer.approvals[id] != pending || command == nil || peer.instance != frame.InstanceID ||
		!c.peerFresh(peer, c.clock.Now()) || peer.activeTurn != value.TurnID || peer.stopped ||
		command.state != leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED ||
		!c.clock.Now().Before(pending.deadline) || r.Context().Err() != nil {
		c.reply(w, map[string]any{"epoch": c.epoch, "fallback": true})
		return
	}
	command.state = leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED
	c.rememberOutcome(peer, command, false)
	c.reply(w, map[string]any{"epoch": c.epoch, "command": command})
}

func (c *nativeControlController) AnswerApproval(ctx context.Context, input agent.NativeSessionApprovalAnswer) error {
	if !validNativeSessionID(input.PromptID) || !validNativeSessionID(input.ExpectedTurnID) ||
		(input.OptionID != "allow_once" && input.OptionID != "deny" && input.OptionID != "original_app") {
		return agent.ErrNativeControlInvalid
	}
	command, err := c.enqueue(ctx, input.SessionID, input.BindingToken, &nativeControlCommand{
		RequestID: input.RequestID, Kind: "approval", TurnID: input.ExpectedTurnID,
		PromptID: input.PromptID, OptionID: input.OptionID})
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return agent.ErrNativeControlUncertain
	case <-command.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		switch command.state {
		case leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED:
			return nil
		case leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN:
			return agent.ErrNativeControlUncertain
		default:
			return agent.ErrNativeControlStale
		}
	}
}
