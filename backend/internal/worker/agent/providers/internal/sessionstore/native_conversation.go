package sessionstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	nativeConversationPageBytes   = 4 << 20
	nativeConversationHeaderBytes = 1 << 20
	nativeConversationHeaderRows  = 200
	nativeConversationMaxRows     = 200
	nativeConversationMaxCursor   = 2048
	nativeConversationMaxSources  = 128
)

var ErrNativeConversationIdentity = errors.New("native conversation session identity is unverified")

// NativeConversationFileQuery is constructed only by a provider. Root and
// RelativePath never come from the browser. ValidateRecord must reject explicit
// foreign identities; true means that this record proves the requested session.
type NativeConversationFileQuery struct {
	Root           string
	RelativePath   string
	SessionID      string
	Format         string
	Query          agent.NativeConversationQuery
	ValidateRecord func([]byte) (bool, error)
}

type nativeConversationCursor struct {
	Version  int
	File     string
	Session  string
	Revision string
	Offset   int64
	Size     int64
	Modified int64
	Anchor   string
}

type nativeConversationGeneration struct {
	identity, header, revision string
	size, modified             int64
	seen                       time.Time
}

var nativeConversationSources = struct {
	sync.Mutex
	key     [32]byte
	sources map[string]nativeConversationGeneration
}{key: newNativeConversationKey(), sources: map[string]nativeConversationGeneration{}}

// Fixed stripes bound lifetime state while serializing source sampling itself,
// not only generation-map writes. A later append sample must never overtake an
// older opened-file snapshot and make it look like a truncation.
var nativeConversationReadLeases = func() [64]chan struct{} {
	var leases [64]chan struct{}
	for index := range leases {
		leases[index] = make(chan struct{}, 1)
		leases[index] <- struct{}{}
	}
	return leases
}()

func acquireNativeConversationRead(ctx context.Context, root, relative string) (func(), error) {
	hash := sha256.Sum256([]byte(filepath.Clean(root) + "\x00" + filepath.ToSlash(relative)))
	lease := nativeConversationReadLeases[int(hash[0])%len(nativeConversationReadLeases)]
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-lease:
		return func() { lease <- struct{}{} }, nil
	}
}

func newNativeConversationKey() (key [32]byte) {
	if _, err := rand.Read(key[:]); err != nil {
		panic("native conversation cursor entropy unavailable")
	}
	return key
}

func nativeConversationHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func nativeConversationRevision(file, identity, header string, info os.FileInfo) string {
	nativeConversationSources.Lock()
	defer nativeConversationSources.Unlock()
	now := time.Now()
	old, found := nativeConversationSources.sources[file]
	if !found || old.identity != identity || old.header != header || info.Size() < old.size ||
		(info.Size() == old.size && info.ModTime().UnixNano() != old.modified) {
		key := newNativeConversationKey()
		old.revision = hex.EncodeToString(key[:16])
	}
	old.identity, old.header = identity, header
	old.size, old.modified, old.seen = info.Size(), info.ModTime().UnixNano(), now
	if !found && len(nativeConversationSources.sources) >= nativeConversationMaxSources {
		var oldest string
		var seen time.Time
		for id, value := range nativeConversationSources.sources {
			if oldest == "" || value.seen.Before(seen) {
				oldest, seen = id, value.seen
			}
		}
		delete(nativeConversationSources.sources, oldest)
	}
	nativeConversationSources.sources[file] = old
	return old.revision
}

func encodeNativeConversationCursor(value nativeConversationCursor) string {
	data, _ := json.Marshal(value)
	mac := hmac.New(sha256.New, nativeConversationSources.key[:])
	_, _ = mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func decodeNativeConversationCursor(encoded string) (nativeConversationCursor, bool) {
	var value nativeConversationCursor
	if len(encoded) == 0 || len(encoded) > nativeConversationMaxCursor {
		return value, false
	}
	parts := bytes.Split([]byte(encoded), []byte("."))
	if len(parts) != 2 {
		return value, false
	}
	data, err := base64.RawURLEncoding.DecodeString(string(parts[0]))
	if err != nil {
		return value, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		return value, false
	}
	mac := hmac.New(sha256.New, nativeConversationSources.key[:])
	_, _ = mac.Write(data)
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(data, &value) != nil ||
		value.Version != 1 || value.Offset < 0 || value.Size < value.Offset {
		return nativeConversationCursor{}, false
	}
	return value, true
}

func nativeReadAt(file *os.File, start, length int64) ([]byte, error) {
	if start < 0 || length < 0 || length > nativeConversationPageBytes {
		return nil, ErrNativeConversationLimit
	}
	data := make([]byte, int(length))
	n, err := file.ReadAt(data, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n != len(data) {
		return nil, ErrNativeConversationUnsafe
	}
	return data, nil
}

func nativeCursorAnchor(file *os.File, offset int64) (string, error) {
	start := max(int64(0), offset-64)
	data, err := nativeReadAt(file, start, offset-start)
	if err != nil {
		return "", err
	}
	if offset > 0 && (len(data) == 0 || data[len(data)-1] != '\n') {
		return "", ErrNativeConversationUnsafe
	}
	return nativeConversationHash(data), nil
}

func nativeValidRecordJSON(raw []byte) bool {
	// Validate skipped fields too: duplicate identity keys must not be reduced
	// to a last-key-wins map before the provider checks session ownership.
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	if err := decoder.SkipValue(); err != nil {
		return false
	}
	_, err := decoder.ReadToken()
	return errors.Is(err, io.EOF)
}

func nativeHeader(ctx context.Context, file *os.File, size int64, validate func([]byte) (bool, error)) (string, error) {
	data, err := nativeReadAt(file, 0, min(size, int64(nativeConversationHeaderBytes)))
	if err != nil {
		return "", err
	}
	found, rows, consumed := false, 0, 0
	var firstLine []byte
	for rows < nativeConversationHeaderRows {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		end := bytes.IndexByte(data[consumed:], '\n')
		if end < 0 {
			// An identity-bearing row may contain a supported inline image and
			// exceed the small header probe. Complete only this boundary row,
			// within the existing per-record/page cap; never validate a prefix.
			if !found && len(data) == nativeConversationHeaderBytes && int64(consumed) < size {
				complete, err := nativeReadAt(file, int64(consumed), min(size-int64(consumed), int64(nativeConversationPageBytes)))
				if err != nil {
					return "", err
				}
				if err := ctx.Err(); err != nil {
					return "", err
				}
				if boundary := bytes.IndexByte(complete, '\n'); boundary >= 0 {
					line := complete[:boundary]
					if !nativeValidRecordJSON(line) {
						return "", ErrNativeConversationIdentity
					}
					matches, err := validate(line)
					if err != nil {
						if ctx.Err() != nil {
							return "", ctx.Err()
						}
						return "", ErrNativeConversationIdentity
					}
					found = matches
					if firstLine == nil {
						firstLine = complete[:boundary+1]
					}
				}
			}
			break
		}
		end += consumed
		line := data[consumed:end]
		if !nativeValidRecordJSON(line) {
			return "", ErrNativeConversationIdentity
		}
		matches, err := validate(line)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", ErrNativeConversationIdentity
		}
		if firstLine == nil {
			firstLine = data[consumed : end+1]
		}
		found = found || matches
		consumed = end + 1
		rows++
		if consumed == len(data) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !found {
		return "", ErrNativeConversationIdentity
	}
	// Appending records into a short header must not change its generation.
	if firstLine == nil {
		return "", ErrNativeConversationIdentity
	}
	return nativeConversationHash(firstLine), nil
}

type nativeFileRecord struct {
	start, end int64
	data       []byte
}

func nativeFileRecords(data []byte, start int64) ([]nativeFileRecord, bool) {
	result := []nativeFileRecord{}
	consumed := 0
	for consumed < len(data) {
		next := bytes.IndexByte(data[consumed:], '\n')
		if next < 0 {
			return result, true
		}
		next += consumed
		result = append(result, nativeFileRecord{start: start + int64(consumed), end: start + int64(next+1), data: data[consumed:next]})
		consumed = next + 1
	}
	return result, false
}

// ReadNativeConversationFile reads complete records from a verified opened file.
// It creates no provider, subprocess, log, database row, or filesystem entry.
func ReadNativeConversationFile(ctx context.Context, q NativeConversationFileQuery) (agent.NativeConversationPage, error) {
	page := agent.NativeConversationPage{Format: q.Format, SessionID: q.SessionID, ObservedAt: time.Now()}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if q.SessionID == "" || q.Query.SessionID != q.SessionID || q.Format == "" || q.ValidateRecord == nil {
		return page, ErrNativeConversationIdentity
	}
	direction := q.Query.Direction
	if direction == "" {
		direction = contracts.NativeConversationDirectionLatest
	}
	if direction != contracts.NativeConversationDirectionLatest && direction != contracts.NativeConversationDirectionBefore && direction != contracts.NativeConversationDirectionAfter {
		page.PartialReason = "unsupported_direction"
		return page, nil
	}
	limit := q.Query.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, nativeConversationMaxRows)
	relative, err := nativeRelative(q.RelativePath)
	if err != nil {
		return page, err
	}
	release, err := acquireNativeConversationRead(ctx, q.Root, relative)
	if err != nil {
		return page, err
	}
	defer release()
	checked, err := nativeOwnedPath(q.Root, relative, false)
	if err != nil {
		return page, err
	}
	rootInfo, err := nativeOwnedPath(q.Root, "", true)
	if err != nil {
		return page, err
	}
	root, err := os.OpenRoot(q.Root)
	if err != nil {
		return page, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(relative)
	if err != nil {
		return page, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || nativeOpenedFileOwned(file) != nil || !opened.Mode().IsRegular() || !os.SameFile(checked, opened) || opened.Size() < 0 || opened.Size() > 1<<50 {
		return page, ErrNativeConversationUnsafe
	}
	identity, err := nativeFileIdentity(file)
	if err != nil {
		return page, err
	}
	header, err := nativeHeader(ctx, file, opened.Size(), q.ValidateRecord)
	if err != nil {
		return page, err
	}
	fileKey := nativeConversationHash([]byte(filepath.Clean(q.Root) + "\x00" + filepath.ToSlash(relative) + "\x00" + q.Format + "\x00" + q.SessionID))
	page.Revision = nativeConversationRevision(fileKey, identity, header, opened)
	offset := opened.Size()
	if direction == contracts.NativeConversationDirectionAfter {
		offset = 0
	}
	if direction != contracts.NativeConversationDirectionLatest {
		cursor, valid := decodeNativeConversationCursor(q.Query.Cursor)
		if !valid || cursor.File != fileKey || cursor.Session != q.SessionID || cursor.Revision != page.Revision ||
			opened.Size() < cursor.Size || (opened.Size() == cursor.Size && opened.ModTime().UnixNano() != cursor.Modified) {
			page.ResetRequired, page.PartialReason = true, "source_or_cursor_changed"
			return page, nil
		}
		anchor, err := nativeCursorAnchor(file, cursor.Offset)
		if err != nil || anchor != cursor.Anchor {
			page.ResetRequired, page.PartialReason = true, "source_or_cursor_changed"
			return page, nil
		}
		offset = cursor.Offset
	} else if q.Query.Cursor != "" {
		page.ResetRequired, page.PartialReason = true, "unexpected_cursor"
		return page, nil
	}
	start, end := int64(0), offset
	if direction == contracts.NativeConversationDirectionAfter {
		start, end = offset, min(opened.Size(), offset+nativeConversationPageBytes)
	} else {
		start = max(int64(0), end-nativeConversationPageBytes)
	}
	data, err := nativeReadAt(file, start, end-start)
	if err != nil {
		return page, err
	}
	if start > 0 && direction != contracts.NativeConversationDirectionAfter {
		previous, err := nativeReadAt(file, start-1, 1)
		if err != nil {
			return page, err
		}
		if previous[0] != '\n' {
			if first := bytes.IndexByte(data, '\n'); first >= 0 {
				start += int64(first + 1)
				data = data[first+1:]
				if len(data) == 0 {
					page.PartialReason = "record_exceeds_read_limit"
				}
			} else {
				page.PartialReason = "record_exceeds_read_limit"
				return page, nil
			}
		}
	}
	records, incomplete := nativeFileRecords(data, start)
	completeEnd := start
	if len(records) > 0 {
		completeEnd = records[len(records)-1].end
	}
	if len(records) == 0 && incomplete && end-start >= nativeConversationPageBytes {
		page.PartialReason = "record_exceeds_read_limit"
		return page, nil
	}
	if len(records) > limit {
		if direction == contracts.NativeConversationDirectionAfter {
			records = records[:limit]
		} else {
			records = records[len(records)-limit:]
		}
	}
	older, newer := offset, offset
	if len(records) > 0 {
		older, newer = records[0].start, records[len(records)-1].end
	} else if direction == contracts.NativeConversationDirectionLatest {
		older, newer = start, start
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		if !nativeValidRecordJSON(record.data) {
			page.Records = nil
			page.PartialReason = "invalid_native_record"
			return page, nil
		}
		if _, err := q.ValidateRecord(record.data); err != nil {
			page.Records = nil
			return page, ErrNativeConversationIdentity
		}
		page.Records = append(page.Records, agent.NativeConversationRecord{ID: fmt.Sprintf("%s:%x", page.Revision, record.start), Seq: record.start + 1, NativeJSON: bytes.Clone(record.data)})
	}
	after, err := file.Stat()
	if err != nil || nativeOpenedFileOwned(file) != nil || !os.SameFile(opened, after) || after.Size() < opened.Size() ||
		(after.Size() == opened.Size() && !after.ModTime().Equal(opened.ModTime())) {
		page.Records = nil
		page.ResetRequired, page.PartialReason = true, "source_changed_during_read"
		return page, nil
	}
	current, err := nativeOwnedPath(q.Root, relative, false)
	currentRoot, rootErr := nativeOwnedPath(q.Root, "", true)
	if err != nil || rootErr != nil || !os.SameFile(after, current) || !os.SameFile(rootInfo, currentRoot) {
		return agent.NativeConversationPage{}, ErrNativeConversationUnsafe
	}
	makeCursor := func(position int64) (string, error) {
		anchor, err := nativeCursorAnchor(file, position)
		if err != nil {
			return "", err
		}
		return encodeNativeConversationCursor(nativeConversationCursor{Version: 1, File: fileKey, Session: q.SessionID, Revision: page.Revision,
			Offset: position, Size: opened.Size(), Modified: opened.ModTime().UnixNano(), Anchor: anchor}), nil
	}
	page.OlderCursor, err = makeCursor(older)
	if err != nil {
		return page, err
	}
	page.NewerCursor, err = makeCursor(newer)
	if err != nil {
		return page, err
	}
	page.HasOlder = older > 0
	page.HasNewer = newer < opened.Size()
	if incomplete && newer == completeEnd && end == opened.Size() {
		page.HasNewer = false
		page.PartialReason = "incomplete_trailing_record"
	}
	return page, nil
}
