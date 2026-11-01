package codexobserve

import (
	"bytes"
	"encoding/json"
	"strings"
)

// A review starts with the action the user is deciding about. Routing IDs and
// progress metadata stay in the separately hashed native request, not in the
// product's approval preview. Unknown fields remain visible in full.
func controlApprovalReview(request controlApprovalRequest, fileChange json.RawMessage) ([]byte, error) {
	switch request.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
	default:
		return json.MarshalIndent(struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}{request.Method, request.Params}, "", "  ")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(request.Params, &fields) != nil || fields == nil {
		return nil, errProtocol
	}
	var review strings.Builder
	addField := func(key, label string) error {
		value, exists := fields[key]
		if !exists {
			return nil
		}
		delete(fields, key)
		return approvalReviewField(&review, label, value)
	}
	if request.Method == "item/commandExecution/requestApproval" {
		var command string
		_ = json.Unmarshal(fields["command"], &command)
		if strings.TrimSpace(command) != "" {
			if err := addField("command", "Command"); err != nil {
				return nil, err
			}
		} else if value, exists := fields["networkApprovalContext"]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			if err := addField("networkApprovalContext", "Network access"); err != nil {
				return nil, err
			}
		} else if err := addField("command", "Command"); err != nil {
			return nil, err
		}
	}
	if request.Method == "item/fileChange/requestApproval" && fileChange != nil {
		if err := approvalFileReview(&review, fileChange); err != nil {
			return nil, err
		}
	}
	if request.Method == "item/permissions/requestApproval" {
		if err := addField("permissions", "Requested permissions"); err != nil {
			return nil, err
		}
	}
	for _, field := range []struct{ key, label string }{
		{"cwd", "Working directory"}, {"reason", "Reason"}, {"networkApprovalContext", "Network access"},
	} {
		if err := addField(field.key, field.label); err != nil {
			return nil, err
		}
	}
	// Omit known envelope metadata only. Nested permission objects, including
	// fields with these names, are preserved rather than filtered recursively.
	for _, key := range []string{"threadId", "turnId", "itemId", "startedAtMs", "environmentId", "kind"} {
		delete(fields, key)
	}
	if decisions, exists := fields["availableDecisions"]; exists {
		delete(fields, "availableDecisions")
		var offered []json.RawMessage
		if json.Unmarshal(decisions, &offered) != nil {
			return nil, errProtocol
		}
		// Choice labels/scopes are already shown by the controls. Preserve any
		// amendment or future structured decision payload as review information.
		var structured []json.RawMessage
		for _, decision := range offered {
			var label string
			if json.Unmarshal(decision, &label) != nil {
				structured = append(structured, decision)
			}
		}
		if len(structured) != 0 {
			raw, err := json.Marshal(structured)
			if err != nil {
				return nil, err
			}
			if err := approvalReviewField(&review, "Additional decision details", raw); err != nil {
				return nil, err
			}
		}
	}
	if len(fields) != 0 {
		raw, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		if err := approvalReviewField(&review, "Additional request details", raw); err != nil {
			return nil, err
		}
	}
	return []byte(review.String()), nil
}

func approvalReviewField(review *strings.Builder, label string, raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return errProtocol
	}
	var content string
	if text, ok := value.(string); ok && !strings.ContainsFunc(text, func(char rune) bool {
		return char < ' ' && char != '\n' && char != '\r' && char != '\t'
	}) {
		content = text
	} else {
		pretty, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		content = string(pretty)
	}
	if review.Len() != 0 {
		review.WriteString("\n\n")
	}
	review.WriteString(label)
	review.WriteByte('\n')
	review.WriteString(content)
	return nil
}

func approvalFileReview(review *strings.Builder, raw json.RawMessage) error {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil || item == nil {
		return errProtocol
	}
	var changes []map[string]json.RawMessage
	if json.Unmarshal(item["changes"], &changes) != nil {
		return errProtocol
	}
	for _, change := range changes {
		if change == nil {
			return errProtocol
		}
		for _, field := range []struct{ key, label string }{
			{"path", "File"}, {"kind", "Change"}, {"diff", "Diff"},
		} {
			if value, exists := change[field.key]; exists {
				if err := approvalReviewField(review, field.label, value); err != nil {
					return err
				}
				delete(change, field.key)
			}
		}
		if len(change) != 0 {
			other, err := json.Marshal(change)
			if err != nil {
				return err
			}
			if err := approvalReviewField(review, "Additional file change details", other); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"id", "type", "status", "changes", "startedAtMs"} {
		delete(item, key)
	}
	if len(item) != 0 {
		other, err := json.Marshal(item)
		if err != nil {
			return err
		}
		return approvalReviewField(review, "Additional file details", other)
	}
	return nil
}
