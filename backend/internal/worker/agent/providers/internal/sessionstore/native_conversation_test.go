package sessionstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeConversationFixture(t *testing.T, count int) (NativeConversationFileQuery, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(root, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sessions"), 0o700))
	path := filepath.Join(root, "sessions", "harmless.jsonl")
	var lines strings.Builder
	for index := 0; index < count; index++ {
		data, err := json.Marshal(map[string]any{"session": "test-session", "text": index})
		require.NoError(t, err)
		lines.Write(data)
		lines.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, []byte(lines.String()), 0o600))
	q := NativeConversationFileQuery{Root: root, RelativePath: filepath.Join("sessions", "harmless.jsonl"), SessionID: "test-session", Format: "test-jsonl",
		Query: agent.NativeConversationQuery{SessionID: "test-session", Direction: contracts.NativeConversationDirectionLatest, Limit: 2},
		ValidateRecord: func(data []byte) (bool, error) {
			var value map[string]any
			if json.Unmarshal(data, &value) != nil || value["session"] != "test-session" {
				return false, ErrNativeConversationIdentity
			}
			return true, nil
		}}
	return q, path
}

func nativeConversationAppend(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func TestNativeConversationPagesAndAppendKeepStableIdentity(t *testing.T) {
	q, path := nativeConversationFixture(t, 5)
	beforeBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	latest, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, latest.Records, 2)
	assert.True(t, latest.HasOlder)
	assert.False(t, latest.HasNewer)
	assert.NotEmpty(t, latest.OlderCursor)
	assert.NotEmpty(t, latest.NewerCursor)
	assert.Less(t, latest.Records[0].Seq, latest.Records[1].Seq)
	assert.JSONEq(t, "{\"session\":\"test-session\",\"text\":3}", string(latest.Records[0].NativeJSON))
	q.Query.Direction, q.Query.Cursor = contracts.NativeConversationDirectionBefore, latest.OlderCursor
	older, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, older.Records, 2)
	assert.Less(t, older.Records[1].Seq, latest.Records[0].Seq)
	q.Query.Direction, q.Query.Cursor = contracts.NativeConversationDirectionAfter, latest.NewerCursor
	empty, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	assert.Empty(t, empty.Records)
	assert.NotEmpty(t, empty.NewerCursor, "EOF must retain a tail anchor")
	afterBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, beforeBytes, afterBytes, "reads must not change transcript bytes")
	nativeConversationAppend(t, path, "{\"session\":\"test-session\",\"text\":5}\n")
	q.Query.Cursor = empty.NewerCursor
	appended, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, appended.Records, 1)
	assert.Equal(t, latest.Revision, appended.Revision)
	assert.Greater(t, appended.Records[0].Seq, latest.Records[1].Seq)
	assert.False(t, appended.ResetRequired)
}

func TestNativeConversationPartialTrailingLineBecomesOneCompleteRecord(t *testing.T) {
	q, path := nativeConversationFixture(t, 2)
	nativeConversationAppend(t, path, "{\"session\":\"test-session\",\"text\":\"later")
	page, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, page.Records, 2)
	assert.Equal(t, "incomplete_trailing_record", page.PartialReason)
	assert.False(t, page.HasNewer)
	nativeConversationAppend(t, path, "\"}\n")
	q.Query.Direction, q.Query.Cursor = contracts.NativeConversationDirectionAfter, page.NewerCursor
	next, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, next.Records, 1)
	assert.JSONEq(t, "{\"session\":\"test-session\",\"text\":\"later\"}", string(next.Records[0].NativeJSON))
	assert.Equal(t, page.Revision, next.Revision)
}

func TestNativeConversationRejectsForgedCrossFileAndNonBoundaryCursors(t *testing.T) {
	q, path := nativeConversationFixture(t, 3)
	page, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	for _, cursor := range []string{"not-a-cursor", page.NewerCursor + "x"} {
		q.Query.Direction, q.Query.Cursor = contracts.NativeConversationDirectionAfter, cursor
		result, err := ReadNativeConversationFile(context.Background(), q)
		require.NoError(t, err)
		assert.True(t, result.ResetRequired)
		assert.Empty(t, result.Records)
	}
	cursor, valid := decodeNativeConversationCursor(page.NewerCursor)
	require.True(t, valid)
	cursor.Offset--
	q.Query.Cursor = encodeNativeConversationCursor(cursor)
	result, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	assert.True(t, result.ResetRequired, "even a server-signed offset must identify a real line boundary")
	copyPath := filepath.Join(filepath.Dir(path), "other.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(copyPath, data, 0o600))
	q.RelativePath = filepath.Join("sessions", "other.jsonl")
	q.Query.Cursor = page.NewerCursor
	result, err = ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	assert.True(t, result.ResetRequired)
}

func TestNativeConversationReplacementAndTruncationReset(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "truncate", true: "replace"}[replacement], func(t *testing.T) {
			q, path := nativeConversationFixture(t, 5)
			page, err := ReadNativeConversationFile(context.Background(), q)
			require.NoError(t, err)
			data := []byte("{\"session\":\"test-session\",\"text\":\"new\"}\n")
			if replacement {
				require.NoError(t, os.Remove(path))
			}
			require.NoError(t, os.WriteFile(path, data, 0o600))
			q.Query.Direction, q.Query.Cursor = contracts.NativeConversationDirectionAfter, page.NewerCursor
			next, err := ReadNativeConversationFile(context.Background(), q)
			require.NoError(t, err)
			assert.True(t, next.ResetRequired)
			assert.Empty(t, next.Records)
			assert.NotEqual(t, page.Revision, next.Revision)
		})
	}
}

func TestNativeConversationRejectsUnknownIdentityAndTraversal(t *testing.T) {
	q, _ := nativeConversationFixture(t, 2)
	q.ValidateRecord = func([]byte) (bool, error) { return false, nil }
	_, err := ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, ErrNativeConversationIdentity)
	q.RelativePath = filepath.Join("..", "outside.jsonl")
	_, err = ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, ErrNativeConversationUnsafe)
}

func TestNativeConversationHeaderAllowsMetadataThenValidatesEveryEmittedRecord(t *testing.T) {
	q, path := nativeConversationFixture(t, 1)
	data := "{\"metadata\":true}\n{\"session\":\"test-session\",\"text\":0}\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	q.ValidateRecord = func(raw []byte) (bool, error) {
		var value map[string]any
		if json.Unmarshal(raw, &value) != nil {
			return false, ErrNativeConversationIdentity
		}
		id, present := value["session"]
		if present && id != "test-session" {
			return false, ErrNativeConversationIdentity
		}
		return present, nil
	}
	page, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, page.Records, 2)
	for index := 0; index < 205; index++ {
		nativeConversationAppend(t, path, "{\"session\":\"test-session\",\"text\":1}\n")
	}
	nativeConversationAppend(t, path, "{\"session\":\"foreign\",\"text\":\"do not return\"}\n")
	page, err = ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, ErrNativeConversationIdentity)
	assert.Empty(t, page.Records)
}

func TestNativeConversationReportsOversizedRecordAndBoundsDirectoryDiscovery(t *testing.T) {
	q, path := nativeConversationFixture(t, 1)
	nativeConversationAppend(t, path, "{\"session\":\"test-session\",\"text\":\""+strings.Repeat("x", nativeConversationPageBytes+1)+"\"}\n")
	page, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	assert.Empty(t, page.Records)
	assert.Equal(t, "record_exceeds_read_limit", page.PartialReason, "oversized records and inaccessible older history must remain explicit")
	for _, name := range []string{"one", "two", "three"} {
		require.NoError(t, os.Mkdir(filepath.Join(q.Root, "sessions", name), 0o700))
	}
	_, err = ReadNativeConversationDirectories(q.Root, "sessions", 2)
	require.ErrorIs(t, err, ErrNativeConversationLimit)
}

func TestNativeConversationMissingFileIsDistinguishable(t *testing.T) {
	q, path := nativeConversationFixture(t, 1)
	require.NoError(t, os.Remove(path))
	_, err := ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestNativeConversationRejectsDuplicateIdentityKeysBeforeProjection(t *testing.T) {
	q, path := nativeConversationFixture(t, 1)
	// A map decoder would silently keep the second session value.
	data := "{\"session\":\"foreign\",\"session\":\"test-session\",\"text\":\"must not project\"}\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	page, err := ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, ErrNativeConversationIdentity)
	assert.Empty(t, page.Records)
}

func TestNativeConversationHeaderCompletesSupportedLargeIdentityRecord(t *testing.T) {
	for _, metadataFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-record", true: "after-metadata"}[metadataFirst], func(t *testing.T) {
			q, path := nativeConversationFixture(t, 0)
			large := "{\"inline_image\":\"" + strings.Repeat("a", 2*nativeConversationHeaderBytes) + "\",\"session\":\"test-session\"}\n"
			prefix := ""
			if metadataFirst {
				prefix = "{\"metadata\":true}\n"
			}
			q.ValidateRecord = func(raw []byte) (bool, error) {
				var value map[string]any
				if json.Unmarshal(raw, &value) != nil {
					return false, ErrNativeConversationIdentity
				}
				if value["metadata"] == true {
					return false, nil
				}
				if value["session"] != "test-session" {
					return false, ErrNativeConversationIdentity
				}
				return true, nil
			}
			require.NoError(t, os.WriteFile(path, []byte(prefix+large), 0o600))
			page, err := ReadNativeConversationFile(context.Background(), q)
			require.NoError(t, err)
			require.NotEmpty(t, page.Records)
			assert.Equal(t, strings.TrimSuffix(large, "\n"), string(page.Records[len(page.Records)-1].NativeJSON))
			assert.Empty(t, page.PartialReason)
			nativeConversationAppend(t, path, "{\"session\":\"test-session\",\"text\":\"later\"}\n")
			q.Query.Direction, q.Query.Cursor = contracts.NativeConversationDirectionAfter, page.NewerCursor
			next, err := ReadNativeConversationFile(context.Background(), q)
			require.NoError(t, err)
			require.Len(t, next.Records, 1)
			assert.Equal(t, page.Revision, next.Revision, "large-row fallback must keep the first complete line generation stable on append")
		})
	}
}

func TestNativeConversationLargeHeaderNeverUsesTruncatedOrForeignIdentity(t *testing.T) {
	for name, line := range map[string]string{
		"foreign":    "{\"inline_image\":\"" + strings.Repeat("a", 2*nativeConversationHeaderBytes) + "\",\"session\":\"foreign\"}\n",
		"malformed":  "{\"inline_image\":\"" + strings.Repeat("a", 2*nativeConversationHeaderBytes) + "\",\"session\":\"test-session\"\n",
		"incomplete": "{\"inline_image\":\"" + strings.Repeat("a", 2*nativeConversationHeaderBytes) + "\",\"session\":\"test-session\"}",
		"oversized":  "{\"inline_image\":\"" + strings.Repeat("a", nativeConversationPageBytes) + "\",\"session\":\"test-session\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			q, path := nativeConversationFixture(t, 0)
			require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
			page, err := ReadNativeConversationFile(context.Background(), q)
			require.ErrorIs(t, err, ErrNativeConversationIdentity)
			assert.Empty(t, page.Records)
		})
	}
}

func TestNativeConversationLargeHeaderFallbackHonorsCancellation(t *testing.T) {
	q, path := nativeConversationFixture(t, 0)
	data := "{\"metadata\":true}\n{\"inline_image\":\"" + strings.Repeat("a", 2*nativeConversationHeaderBytes) + "\",\"session\":\"test-session\"}\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var validated int
	q.ValidateRecord = func([]byte) (bool, error) {
		validated++
		cancel()
		return false, nil
	}
	page, err := ReadNativeConversationFile(ctx, q)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, page.Records)
	assert.Equal(t, 1, validated, "cancellation between header rows must prevent large fallback validation")
}

type nativeConversationWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *nativeConversationWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestNativeConversationSerializesSamplingAcrossConcurrentAppend(t *testing.T) {
	q, path := nativeConversationFixture(t, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	validate := q.ValidateRecord
	q.ValidateRecord = func(raw []byte) (bool, error) {
		if first.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		return validate(raw)
	}
	type outcome struct {
		page agent.NativeConversationPage
		err  error
	}
	oldRead := make(chan outcome, 1)
	go func() {
		page, err := ReadNativeConversationFile(ctx, q)
		oldRead <- outcome{page, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first read never reached its opened-file header")
	}
	nativeConversationAppend(t, path, "{\"session\":\"test-session\",\"text\":\"appended\"}\n")
	blockedCtx, cancelBlocked := context.WithCancel(ctx)
	defer cancelBlocked()
	waiting := &nativeConversationWaitingContext{Context: blockedCtx, waiting: make(chan struct{})}
	var secondValidated atomic.Bool
	second := q
	second.ValidateRecord = func(raw []byte) (bool, error) {
		secondValidated.Store(true)
		return validate(raw)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := ReadNativeConversationFile(waiting, second)
		blocked <- err
	}()
	select {
	case <-waiting.waiting:
	case <-ctx.Done():
		t.Fatal("second read never attempted the cancellable source lease")
	}
	assert.False(t, secondValidated.Load(), "a later read must not sample the appended file before the first read releases its source")
	cancelBlocked()
	select {
	case err := <-blocked:
		require.ErrorIs(t, err, context.Canceled)
	case <-ctx.Done():
		t.Fatal("waiting read did not honor cancellation")
	}
	close(release)
	var before outcome
	select {
	case before = <-oldRead:
	case <-ctx.Done():
		t.Fatal("first read did not finish")
	}
	require.NoError(t, before.err)
	after, err := ReadNativeConversationFile(ctx, second)
	require.NoError(t, err)
	assert.Equal(t, before.page.Revision, after.Revision, "an append sampled after the earlier read must not rotate generation")
	assert.False(t, after.ResetRequired)
	require.NotEmpty(t, after.Records)
	assert.Contains(t, string(after.Records[len(after.Records)-1].NativeJSON), "appended")
}
