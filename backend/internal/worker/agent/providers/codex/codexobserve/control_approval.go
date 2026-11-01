package codexobserve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var _ agent.NativeSessionApprovalController = (*Controller)(nil)

// Request IDs are app-server JSON-RPC IDs: both strings and integers occur. Keep
// their native type on the private wire; the browser receives only a digest.
type controlApprovalRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type controlApprovalAction struct {
	prompt    agent.NativeSessionApproval
	journalID string
	id        json.RawMessage
	method    string
	values    map[string]any
}

func controlMutationMethod(method string) bool {
	switch method {
	case "thread-follower-start-turn", "thread-follower-interrupt-turn",
		"thread-follower-command-approval-decision", "thread-follower-file-approval-decision",
		"thread-follower-permissions-request-approval-response":
		return true
	default:
		return false
	}
}

func controlApprovalID(id json.RawMessage) bool {
	if !utf8.Valid(id) {
		return false
	}
	var text string
	if json.Unmarshal(id, &text) == nil {
		return text != "" && len(text) <= contracts.NativeSessionControlMaxApprovalTextBytes && !strings.ContainsRune(text, 0)
	}
	value := string(id)
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return err == nil && number <= 9007199254740991
}

func canonicalApproval(request controlApprovalRequest, fileChange json.RawMessage) ([]byte, error) {
	raw, err := json.Marshal(struct {
		Request    controlApprovalRequest `json:"request"`
		FileChange json.RawMessage        `json:"fileChange,omitempty"`
	}{request, fileChange})
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func controlApprovalKey(id json.RawMessage) string {
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (c *Controller) approvalAction(snapshot controlSnapshot, request controlApprovalRequest) controlApprovalAction {
	action := controlApprovalAction{prompt: agent.NativeSessionApproval{Title: "Native Codex prompt",
		UnavailableReason: "Answer this prompt in the original Codex app."}, values: make(map[string]any)}
	var params struct {
		ThreadID           string            `json:"threadId"`
		TurnID             string            `json:"turnId"`
		ItemID             string            `json:"itemId"`
		AvailableDecisions []json.RawMessage `json:"availableDecisions"`
		Permissions        json.RawMessage   `json:"permissions"`
		Command            string            `json:"command"`
		NetworkContext     json.RawMessage   `json:"networkApprovalContext"`
	}
	validParams := utf8.Valid(request.Params) && json.Unmarshal(request.Params, &params) == nil
	var fileChange json.RawMessage
	if request.Method == "item/fileChange/requestApproval" {
		fileChange = snapshot.approvalFileItem(params.TurnID, params.ItemID)
	}
	raw, err := canonicalApproval(request, fileChange)
	if err != nil {
		action.prompt.PromptID = controlBindingHash(c.epoch + "invalid-approval")
		return action
	}
	action.prompt.PromptID = controlBindingHash(c.epoch + "\x00" + snapshot.ownerGeneration + "\x00" + snapshot.ID + "\x00" + string(raw))
	action.journalID = controlBindingHash(snapshot.ownerGeneration + "\x00" + snapshot.ID + "\x00" + string(raw))
	detail, detailErr := controlApprovalReview(request, fileChange)
	if detailErr != nil || len(detail) > contracts.NativeSessionControlMaxApprovalDetailBytes {
		action.prompt.Detail = "This native prompt exceeds the complete-preview limit. Review it in the original Codex app."
		action.prompt.UnavailableReason = "The complete prompt cannot be displayed safely here."
		return action
	}
	action.prompt.Detail = string(detail)
	if !validParams || !controlApprovalID(request.ID) || params.ThreadID != snapshot.ID ||
		!uuidPattern.MatchString(params.TurnID) || params.TurnID != snapshot.activeTurn() || snapshot.Runtime.Type != "active" {
		action.prompt.UnavailableReason = "The native prompt is not bound to this chat's current active turn."
		return action
	}
	action.prompt.TurnID = params.TurnID
	action.id = request.ID
	add := func(id, label, description string, value any) {
		action.prompt.Options = append(action.prompt.Options, agent.NativeSessionApprovalOption{ID: id, Label: label, Description: description})
		action.values[id] = value
	}
	switch request.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if params.ItemID == "" || len(params.ItemID) > contracts.NativeSessionControlMaxApprovalTextBytes || strings.ContainsRune(params.ItemID, 0) {
			return action
		}
		action.prompt.Title = "Approve command execution"
		action.method = "thread-follower-command-approval-decision"
		if request.Method == "item/commandExecution/requestApproval" && strings.TrimSpace(params.Command) == "" {
			var network struct {
				Host     string `json:"host"`
				Protocol string `json:"protocol"`
			}
			if json.Unmarshal(params.NetworkContext, &network) != nil || strings.TrimSpace(network.Host) == "" || strings.TrimSpace(network.Protocol) == "" {
				action.prompt.UnavailableReason = "The complete command or network destination is unavailable. Review this prompt in the original Codex app."
				return action
			}
			action.prompt.Title = "Approve network access"
		}
		if request.Method == "item/fileChange/requestApproval" {
			action.prompt.Title = "Approve file changes"
			action.method = "thread-follower-file-approval-decision"
			if fileChange == nil {
				action.prompt.UnavailableReason = "The complete proposed file changes are unavailable. Review this prompt in the original Codex app."
				return action
			}
		}
		// The public v2 API specifies accept/decline when availableDecisions is
		// absent. When supplied, offer only those exact native decisions. Never
		// infer a persistent/session grant or an exec-policy amendment.
		allowed := map[string]bool{"accept": true, "decline": true}
		if params.AvailableDecisions != nil {
			allowed = make(map[string]bool)
			for _, raw := range params.AvailableDecisions {
				var decision string
				if json.Unmarshal(raw, &decision) == nil && (decision == "accept" || decision == "decline" || decision == "cancel") {
					allowed[decision] = true
				}
			}
		}
		if allowed["accept"] {
			add("allow_once", "Allow once", "Approve only this pending native request.", "accept")
		}
		if allowed["decline"] {
			add("deny", "Deny", "Decline this pending native request.", "decline")
		}
		if allowed["cancel"] {
			add("cancel", "Cancel", "Cancel this pending request and stop its current turn.", "cancel")
		}
	case "item/permissions/requestApproval":
		action.prompt.Title = "Approve requested permissions"
		if !controlApprovalPermissions(params.Permissions) {
			return action
		}
		action.method = "thread-follower-permissions-request-approval-response"
		add("allow_turn", "Allow for this turn", "Grant exactly the displayed permissions for the current turn only.",
			map[string]any{"permissions": params.Permissions, "scope": "turn"})
		add("deny", "Deny", "Grant none of the requested permissions.", map[string]any{"permissions": map[string]any{}, "scope": "turn"})
	default:
		return action
	}
	if len(action.prompt.Options) != 0 {
		action.prompt.CanAnswer = true
		action.prompt.UnavailableReason = ""
	}
	return action
}

func (snapshot controlSnapshot) approvalFileItem(turnID, itemID string) json.RawMessage {
	var result json.RawMessage
	count := 0
	consider := func(turn controlTurn) {
		if turn.ID != turnID || turn.Status != "inProgress" {
			return
		}
		for _, raw := range turn.Items {
			var item struct {
				ID      string            `json:"id"`
				Type    string            `json:"type"`
				Status  string            `json:"status"`
				Changes []json.RawMessage `json:"changes"`
			}
			if !utf8.Valid(raw) || json.Unmarshal(raw, &item) != nil || item.ID != itemID {
				continue
			}
			count++
			if item.Type == "fileChange" && item.Status == "inProgress" && len(item.Changes) != 0 {
				result = raw
			}
		}
	}
	if snapshot.TurnHistory.Kind == "canonical" {
		for _, turn := range snapshot.TurnHistory.History.Entities {
			consider(turn)
		}
	} else {
		for _, turn := range snapshot.Turns {
			consider(turn)
		}
	}
	if count != 1 {
		return nil
	}
	return result
}

func controlApprovalPermissions(raw json.RawMessage) bool {
	// Grant only a verified, documented permissions object; preserve its exact
	// requested values. Unknown permission kinds remain native-only.
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil || len(object) == 0 {
		return false
	}
	for key, value := range object {
		switch key {
		case "network":
			var network map[string]json.RawMessage
			if json.Unmarshal(value, &network) != nil || len(network) != 1 {
				return false
			}
			var enabled bool
			if bytes.Equal(bytes.TrimSpace(network["enabled"]), []byte("null")) || json.Unmarshal(network["enabled"], &enabled) != nil {
				return false
			}
		case "fileSystem":
			var files map[string]json.RawMessage
			if json.Unmarshal(value, &files) != nil || files == nil {
				return false
			}
			for mode, list := range files {
				var paths []string
				if (mode != "read" && mode != "write") || json.Unmarshal(list, &paths) != nil || paths == nil {
					return false
				}
				for _, path := range paths {
					if path == "" || strings.ContainsRune(path, 0) {
						return false
					}
				}
			}
		default:
			return false
		}
	}
	return true
}

func (c *Controller) pendingApprovals(snapshot controlSnapshot) []agent.NativeSessionApproval {
	if len(snapshot.Requests) > contracts.NativeSessionControlMaxPendingApprovals {
		return []agent.NativeSessionApproval{{PromptID: controlBindingHash(c.epoch + "approval-overflow"), Title: "Native Codex prompts",
			Detail:            fmt.Sprintf("%d native prompts are pending. Review them in the original Codex app.", len(snapshot.Requests)),
			UnavailableReason: "There are too many pending prompts to display a complete approval list."}}
	}
	counts := make(map[string]int)
	for _, request := range snapshot.Requests {
		counts[controlApprovalKey(request.ID)]++
	}
	result := make([]agent.NativeSessionApproval, 0, len(snapshot.Requests))
	for _, request := range snapshot.Requests {
		action := c.approvalAction(snapshot, request)
		if counts[controlApprovalKey(request.ID)] != 1 {
			action.prompt.CanAnswer = false
			action.prompt.Options = nil
			action.prompt.UnavailableReason = "The native request identifier is ambiguous. Review it in the original Codex app."
		}
		result = append(result, action.prompt)
	}
	return result
}

func approvalJournalKey(promptID string) (string, string) {
	return "approval-prompt-" + promptID, controlBindingHash("native-approval-prompt\x00" + promptID)
}

func controlApprovalsUnavailable(prompts []agent.NativeSessionApproval, reason string) {
	for index := range prompts {
		prompts[index].CanAnswer = false
		prompts[index].Options = nil
		if prompts[index].UnavailableReason == "" {
			prompts[index].UnavailableReason = reason
		}
	}
}

func (c *Controller) approvalTombstones(snapshot controlSnapshot, prompts []agent.NativeSessionApproval) {
	keys := make(map[string]string)
	for _, request := range snapshot.Requests {
		action := c.approvalAction(snapshot, request)
		keys[action.prompt.PromptID] = action.journalID
	}
	for index := range prompts {
		prompt := &prompts[index]
		if !prompt.CanAnswer {
			continue
		}
		key, digest := approvalJournalKey(keys[prompt.PromptID])
		record, err := c.journal.existing(key, digest)
		if record == nil && err == nil {
			continue
		}
		prompt.CanAnswer = false
		prompt.Options = nil
		prompt.UnavailableReason = "An earlier answer could not be confirmed. Check the original Codex app; this prompt will not be resent."
		if err == nil && record.Status == "accepted" {
			prompt.UnavailableReason = "An answer was already submitted to the original Codex app. This prompt will not be resent."
		}
	}
}

func (c *Controller) AnswerApproval(ctx context.Context, input agent.NativeSessionApprovalAnswer) error {
	if len(input.PromptID) != 64 || input.OptionID == "" || !uuidPattern.MatchString(input.ExpectedTurnID) {
		return agent.ErrNativeControlInvalid
	}
	return c.mutate(ctx, input.RequestID, input.SessionID, input.BindingToken, struct {
		Kind  string
		Input agent.NativeSessionApprovalAnswer
	}{"approval-answer", input}, func(ctx context.Context, s *controlConnection, state controlSnapshot) error {
		if len(state.Requests) > contracts.NativeSessionControlMaxPendingApprovals || state.activeTurn() != input.ExpectedTurnID {
			return agent.ErrNativeControlStale
		}
		var selected *controlApprovalAction
		for _, request := range state.Requests {
			action := c.approvalAction(state, request)
			if action.prompt.PromptID == input.PromptID {
				if selected != nil {
					return agent.ErrNativeControlStale
				}
				selected = &action
			}
		}
		if selected == nil || !selected.prompt.CanAnswer || selected.prompt.TurnID != input.ExpectedTurnID {
			return agent.ErrNativeControlStale
		}
		// A duplicated native ID must not answer either occurrence, even if the
		// payload hashes differ. Native owner lookup uses only this ID.
		matches := 0
		for _, request := range state.Requests {
			if controlApprovalKey(request.ID) == controlApprovalKey(selected.id) {
				matches++
			}
		}
		value, exists := selected.values[input.OptionID]
		if !exists || matches != 1 {
			return agent.ErrNativeControlInvalid
		}
		// A browser reload can produce a different mutation ID. Protect the
		// native one-shot prompt independently of that client ID so an uncertain
		// dispatch cannot be replayed, including with a changed decision.
		key, digest := approvalJournalKey(selected.journalID)
		prior, err := c.journal.existing(key, digest)
		if err != nil {
			return err
		}
		if prior != nil {
			if prior.Status == "accepted" {
				return agent.ErrNativeControlStale
			}
			return agent.ErrNativeControlUncertain
		}
		lease, err := c.binding(input.BindingToken, input.SessionID)
		if err != nil {
			return err
		}
		if err := c.journal.claimApproval(key, digest, input.BindingToken, lease.expires); err != nil {
			return err
		}
		params := map[string]any{"conversationId": input.SessionID, "requestId": selected.id}
		if selected.method == "thread-follower-permissions-request-approval-response" {
			params["response"] = value
		} else {
			params["decision"] = value
		}
		result, err := s.request(ctx, selected.method, 1, params, s.owner)
		if err != nil {
			return c.journal.finish(key, digest, input.BindingToken, lease.expires, err)
		}
		var response struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(result, &response) != nil || !response.OK {
			return c.journal.finish(key, digest, input.BindingToken, lease.expires, agent.ErrNativeControlUncertain)
		}
		// Native owner handlers acknowledge routing even if a competing local
		// answer already removed the request. This receipt means submitted to the
		// owner, never confirmation that a command ran or our answer won a race.
		return c.journal.finish(key, digest, input.BindingToken, lease.expires, nil)
	})
}
