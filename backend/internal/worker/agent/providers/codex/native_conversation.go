package codex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

var codexConversationID = regexp.MustCompile("(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")

func readNativeConversation(ctx context.Context, query agent.NativeConversationQuery) (agent.NativeConversationPage, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return agent.NativeConversationPage{}, errors.New("native home unavailable")
	}
	stored := agent.StoredSessionQuery{HomeDir: home, Getenv: os.Getenv}
	return readCodexNativeConversationAt(ctx, query, codexHome(stored), codexStateDBPath(stored))
}

func readCodexNativeConversationAt(ctx context.Context, query agent.NativeConversationQuery, home, index string) (agent.NativeConversationPage, error) {
	page := agent.NativeConversationPage{Format: contracts.NativeConversationFormatCodexRolloutJSONL, SessionID: query.SessionID, ObservedAt: time.Now()}
	refuse := func(reason string) (agent.NativeConversationPage, error) {
		page.PartialReason = reason
		return page, errors.New(reason)
	}
	if !codexConversationID.MatchString(query.SessionID) {
		return refuse("invalid_session_identity")
	}
	if !filepath.IsAbs(home) || !filepath.IsAbs(index) {
		return refuse("native_index_unavailable")
	}
	if err := sessionstore.VerifyNativeConversationFilePath(filepath.Dir(index), filepath.Base(index)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return page, os.ErrNotExist
		}
		return refuse("native_index_unavailable")
	}
	db, err := sessionstore.OpenDB(ctx, index)
	if err != nil {
		return refuse("native_index_unavailable")
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	connection, err := db.Conn(ctx)
	if err != nil {
		return refuse("native_index_unavailable")
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return refuse("native_index_unavailable")
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA trusted_schema=OFF"); err != nil {
		return refuse("native_index_unavailable")
	}
	var tableType string
	if err := connection.QueryRowContext(ctx, "SELECT type FROM sqlite_schema WHERE name='threads'").Scan(&tableType); err != nil || tableType != "table" {
		return refuse("unsupported_native_index")
	}
	columns, err := connection.QueryContext(ctx, "PRAGMA table_info(threads)")
	if err != nil {
		return refuse("unsupported_native_index")
	}
	shapes := map[string]string{}
	for columns.Next() {
		var id, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := columns.Scan(&id, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			_ = columns.Close()
			return refuse("unsupported_native_index")
		}
		shapes[name] = strings.ToUpper(kind)
	}
	schemaErr := columns.Err()
	_ = columns.Close()
	if schemaErr != nil {
		return refuse("unsupported_native_index")
	}
	for name, kind := range map[string]string{"id": "TEXT", "rollout_path": "TEXT", "cwd": "TEXT", "name": "TEXT", "archived": "INTEGER"} {
		if shapes[name] != kind {
			return refuse("unsupported_native_index")
		}
	}
	// Archived sessions are readable too. Unlike the resume picker this query
	// does not activate or resume anything.
	rows, err := connection.QueryContext(ctx, "SELECT id,rollout_path,cwd,name FROM threads WHERE id=? LIMIT 2", query.SessionID)
	if err != nil {
		return refuse("native_index_unavailable")
	}
	var id, rollout, cwd string
	var name sql.NullString
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&id, &rollout, &cwd, &name); err != nil {
			_ = rows.Close()
			return refuse("unsupported_native_index")
		}
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr == nil && count == 0 {
		return page, os.ErrNotExist
	}
	if readErr != nil || count != 1 || id != query.SessionID {
		return refuse("native_session_not_found")
	}
	// Codex's Windows index uses verbatim local-drive paths while the native
	// session metadata can use ordinary drive paths. Normalize only that spelling
	// before the existing scope/owner/link and identity checks.
	var validPath bool
	rollout, validPath = codexNativeConversationPath(rollout)
	if !validPath {
		return refuse("native_session_path_unverified")
	}
	cwd, validPath = codexNativeConversationPath(cwd)
	if !validPath {
		return refuse("native_session_path_unverified")
	}
	if !filepath.IsAbs(rollout) || !filepath.IsAbs(cwd) || filepath.Clean(rollout) != rollout {
		return refuse("native_session_path_unverified")
	}
	relative, err := filepath.Rel(home, rollout)
	if err != nil || !filepath.IsLocal(relative) {
		return refuse("native_session_path_unverified")
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 2 || (parts[0] != "sessions" && parts[0] != "archived_sessions") || filepath.Ext(rollout) != ".jsonl" {
		return refuse("native_session_path_unverified")
	}
	validate := func(raw []byte) (bool, error) {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil || envelope == nil {
			return false, sessionstore.ErrNativeConversationIdentity
		}
		var kind string
		if json.Unmarshal(envelope["type"], &kind) != nil {
			return false, sessionstore.ErrNativeConversationIdentity
		}
		if kind != "session_meta" {
			return false, nil
		}
		var payload map[string]json.RawMessage
		var nativeID, nativeCWD string
		if json.Unmarshal(envelope["payload"], &payload) != nil || json.Unmarshal(payload["id"], &nativeID) != nil ||
			nativeID != query.SessionID || json.Unmarshal(payload["cwd"], &nativeCWD) != nil {
			return false, sessionstore.ErrNativeConversationIdentity
		}
		nativeCWD, validPath := codexNativeConversationPath(nativeCWD)
		if !validPath || !filepath.IsAbs(nativeCWD) || !sessionstore.SameDir(nativeCWD, cwd) {
			return false, sessionstore.ErrNativeConversationIdentity
		}
		return true, nil
	}
	result, err := sessionstore.ReadNativeConversationFile(ctx, sessionstore.NativeConversationFileQuery{
		Root: home, RelativePath: relative, SessionID: query.SessionID, Format: page.Format, Query: query, ValidateRecord: validate,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return page, err
		}
		return refuse("native_transcript_unavailable")
	}
	if sessionstore.VerifyNativeConversationFilePath(filepath.Dir(index), filepath.Base(index)) != nil {
		return refuse("native_index_changed")
	}
	result.WorkingDir = cwd
	title := strings.Join(strings.FieldsFunc(name.String, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) }), " ")
	runes := []rune(title)
	if len(runes) > 160 {
		runes = runes[:160]
	}
	result.DisplayName = string(runes)
	return result, nil
}
