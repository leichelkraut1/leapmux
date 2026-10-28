package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const claudeConversationProjectLimit = 2048

var errClaudeConversationIdentity = errors.New("claude conversation identity is unavailable or inconsistent")

// readClaudeNativeConversation reads only the selected provider-owned file. It
// neither invokes the CLI nor imports/replays history through a running agent.
// Native branch selection needs the full file before paging (Anthropic SDK
// v0.2.170, _build_conversation_chain/_entries_to_session_messages):
// https://github.com/anthropics/claude-agent-sdk-python/blob/v0.2.170/src/claude_agent_sdk/_internal/sessions.py
// These bounded raw pages therefore retain chronological records explicitly.
func readClaudeNativeConversation(ctx context.Context, q agent.NativeConversationQuery) (agent.NativeConversationPage, error) {
	return readClaudeNativeConversationAt(ctx, claudeConfigDir(agent.StoredSessionQuery{}), q)
}

func readClaudeNativeConversationAt(ctx context.Context, root string, q agent.NativeConversationQuery) (agent.NativeConversationPage, error) {
	if root == "" || !validNativeSessionID(q.SessionID) {
		return agent.NativeConversationPage{}, errClaudeConversationIdentity
	}
	projects, err := sessionstore.ReadNativeConversationDirectories(root, claudeProjectsDirName, claudeConversationProjectLimit)
	if err != nil {
		return agent.NativeConversationPage{}, err
	}
	var found *agent.NativeConversationPage
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return agent.NativeConversationPage{}, err
		}
		var cwd string
		var titles claudeTitleCandidates
		validate := func(raw []byte) (bool, error) {
			raw = bytes.TrimSpace(raw)
			var record claudeTranscriptRecord
			if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &record) != nil || record.IsSidechain {
				return false, errClaudeConversationIdentity
			}
			if record.SessionID != "" && record.SessionID != q.SessionID {
				return false, errClaudeConversationIdentity
			}
			if (record.Type == "user" || record.Type == "assistant") && record.SessionID != q.SessionID {
				return false, errClaudeConversationIdentity
			}
			if record.Cwd != "" {
				if !filepath.IsAbs(record.Cwd) || (cwd != "" && !sessionstore.SameDir(cwd, record.Cwd)) {
					return false, errClaudeConversationIdentity
				}
				// The mangling is lossy, so retain and compare the exact recorded
				// cwd too; a directory name by itself is never session identity.
				mangled := mangleClaudePath(filepath.Clean(record.Cwd))
				projectName := project
				if runtime.GOOS == "windows" {
					projectName, mangled = strings.ToLower(projectName), strings.ToLower(mangled)
				}
				if (len(mangled) <= claudeMangleMaxLength && projectName != mangled) ||
					(len(mangled) > claudeMangleMaxLength && !strings.HasPrefix(projectName, mangled[:claudeMangleMaxLength]+"-")) {
					return false, errClaudeConversationIdentity
				}
				cwd = record.Cwd
			}
			titles.take(record)
			return record.SessionID == q.SessionID, nil
		}
		page, err := sessionstore.ReadNativeConversationFile(ctx, sessionstore.NativeConversationFileQuery{
			Root: root, RelativePath: filepath.Join(claudeProjectsDirName, project, q.SessionID+".jsonl"),
			SessionID: q.SessionID, Format: contracts.NativeConversationFormatClaudeJSONL,
			Query: q, ValidateRecord: validate,
		})
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return agent.NativeConversationPage{}, err
		}
		if found != nil || cwd == "" {
			// Duplicate UUIDs are ambiguous, including identical copied files.
			return agent.NativeConversationPage{}, errClaudeConversationIdentity
		}
		page.WorkingDir = cwd
		page.DisplayName = sessionstore.TrimTitle(titles.best(""))
		const coverage = "Stored main-session records are chronological; branch selection and separate subagent transcripts are not reconstructed."
		if page.PartialReason == "" {
			page.PartialReason = coverage
		} else {
			page.PartialReason += " " + coverage
		}
		found = &page
	}
	if found == nil {
		return agent.NativeConversationPage{}, os.ErrNotExist
	}
	return *found, nil
}
