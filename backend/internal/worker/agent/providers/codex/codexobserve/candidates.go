package codexobserve

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

const candidateLimit = 128

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var errCandidates = errors.New("native status metadata unavailable")

type candidate struct {
	ID   string
	Name string
}
type candidateSet struct {
	Candidates []candidate
	Truncated  bool
}

// metadataPaths reads only this Worker's own configured environment. It never
// discovers another process's environment, arguments, or private runtime files.
func metadataPaths() (string, string, error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", "", errCandidates
		}
		home = filepath.Join(userHome, ".codex")
	}
	if !filepath.IsAbs(home) {
		return "", "", errCandidates
	}
	sqliteHome := os.Getenv("CODEX_SQLITE_HOME")
	if sqliteHome == "" {
		sqliteHome = home
	}
	if !filepath.IsAbs(sqliteHome) {
		return "", "", errCandidates
	}
	return filepath.Clean(home), filepath.Join(sqliteHome, "state_5.sqlite"), nil
}

// readCandidates reads ID/name/order metadata only. The schema is pinned to the
// shipped Codex state_5 metadata layout. Legacy title, preview, first_user_message,
// rollout paths, thread contents, and credentials are never queried.
func readCandidates(ctx context.Context, path string) (candidateSet, error) {
	return readCandidatesVerified(ctx, path, VerifyStatePath)
}
func readCandidatesVerified(ctx context.Context, path string, verify func(string) error) (candidateSet, error) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if err := verify(path); err != nil {
		return candidateSet{}, errCandidates
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return candidateSet{}, errCandidates
	}
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "trusted_schema(0)")
	query.Add("_pragma", "busy_timeout(50)")
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return candidateSet{}, errCandidates
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return candidateSet{}, errCandidates
	}
	defer func() { _ = conn.Close() }()
	// Reject a substituted view before selecting any records.
	var tableType string
	if err := conn.QueryRowContext(ctx, "SELECT type FROM sqlite_schema WHERE name = 'threads'").Scan(&tableType); err != nil || tableType != "table" {
		return candidateSet{}, errCandidates
	}
	schema, err := conn.QueryContext(ctx, "PRAGMA table_info(threads)")
	if err != nil {
		return candidateSet{}, errCandidates
	}
	columns := map[string]string{}
	for schema.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := schema.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = schema.Close()
			return candidateSet{}, errCandidates
		}
		columns[name] = strings.ToUpper(kind)
	}
	err = schema.Err()
	if closeErr := schema.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return candidateSet{}, errCandidates
	}
	for name, kind := range map[string]string{"id": "TEXT", "name": "TEXT", "archived": "INTEGER", "updated_at_ms": "INTEGER", "recency_at_ms": "INTEGER"} {
		if columns[name] != kind {
			return candidateSet{}, errCandidates
		}
	}
	rows, err := conn.QueryContext(ctx, `SELECT id, substr(name,1,160), updated_at_ms, recency_at_ms
FROM threads WHERE archived=0 ORDER BY recency_at_ms DESC,id DESC LIMIT 129`)
	if err != nil {
		return candidateSet{}, errCandidates
	}
	defer func() { _ = rows.Close() }()
	result := candidateSet{}
	count := 0
	for rows.Next() {
		count++
		var id string
		var name sql.NullString
		var updated, recency int64
		if err := rows.Scan(&id, &name, &updated, &recency); err != nil {
			return candidateSet{}, errCandidates
		}
		if count > candidateLimit {
			result.Truncated = true
			continue
		}
		if !uuidPattern.MatchString(id) {
			result.Truncated = true
			continue
		}
		result.Candidates = append(result.Candidates, candidate{ID: id, Name: normalizeName(name.String)})
	}
	if rows.Err() != nil || ctx.Err() != nil {
		return candidateSet{}, errCandidates
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || verify(path) != nil {
		return candidateSet{}, errCandidates
	}
	return result, nil
}
func normalizeName(name string) string {
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	}), " ")
	runes := []rune(name)
	if len(runes) > 160 {
		runes = runes[:160]
	}
	return string(runes)
}
