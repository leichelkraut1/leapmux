package codexobserve

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testThread = "11111111-1111-4111-8111-111111111111"
const testOwner = "22222222-2222-4222-8222-222222222222"
const testClient = "33333333-3333-4333-8333-333333333333"

func framedText(text string) io.Reader {
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(text)))
	return io.MultiReader(bytes.NewReader(header[:]), strings.NewReader(text))
}
func snapshotText(runtime string) string {
	return `{"type":"broadcast","method":"thread-stream-state-changed","version":11,"sourceClientId":"` + testOwner + `","targetClientIds":["` + testClient + `"],"params":{"hostId":"local","conversationId":"` + testThread + `","change":{"type":"snapshot","revision":3,"conversationState":{"id":"` + testThread + `","resumeState":"resumed","threadRuntimeStatus":` + runtime + `}}}}`
}
func TestReadFrameProjectsRuntimeOnly(t *testing.T) {
	text := strings.Replace(snapshotText(`{"type":"active","activeFlags":["waitingOnApproval"]}`), `"resumeState"`, `"turns":[{"private":"never retain this content"}],"resumeState"`, 1)
	f, err := readFrame(framedText(text))
	require.NoError(t, err)
	assert.Equal(t, "waiting", f.Change.State.status())
	encoded, err := json.Marshal(f)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "never retain")
	assert.Equal(t, int64(3), f.Change.Revision)
}

type repeatingReader struct{ left int64 }

func (r *repeatingReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := min(len(p), int(r.left), 8192)
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	r.left -= int64(n)
	return n, nil
}
func largeSnapshot(size int64) io.Reader {
	text := snapshotText(`{"type":"active","activeFlags":[]}`)
	prefix, suffix, _ := strings.Cut(text, `"resumeState"`)
	prefix += `"turns":[{"text":"`
	suffix = `"}],"resumeState"` + suffix
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(int64(len(prefix)+len(suffix))+size))
	return io.MultiReader(bytes.NewReader(header[:]), strings.NewReader(prefix), &repeatingReader{left: size}, strings.NewReader(suffix))
}
func TestReadFrameSkipsLargeSinglePrivateString(t *testing.T) {
	// The fixture streams a production-sized string without constructing it in
	// the test. jsontext may transiently buffer the whole string token; the
	// returned projection and watcher cache must not retain its contents.
	f, err := readFrame(largeSnapshot(35 << 20))
	require.NoError(t, err)
	assert.Equal(t, "working", f.Change.State.status())
	assert.Empty(t, f.Change.Patches)
}
func BenchmarkReadFrameSkipsPrivateContent(b *testing.B) {
	for _, size := range []int64{1 << 20, 35 << 20} {
		b.Run(strconv.FormatInt(size/(1<<20), 10)+"MiB", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := readFrame(largeSnapshot(size)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
func TestReadFrameRejectsUnvalidatedFrames(t *testing.T) {
	valid := snapshotText(`{"type":"idle"}`)
	cases := map[string]string{
		"duplicate selected key":  strings.Replace(valid, `"version":11`, `"version":11,"version":11`, 1),
		"duplicate private key":   strings.Replace(valid, `"resumeState"`, `"turns":[{"secret":1,"secret":2}],"resumeState"`, 1),
		"trailing second value":   valid + `{}`,
		"malformed skipped value": strings.Replace(valid, `"resumeState"`, `"turns":[1,],"resumeState"`, 1),
		"fractional revision":     strings.Replace(valid, `"revision":3`, `"revision":3.5`, 1),
		"missing bytes":           valid[:len(valid)-1],
		"excessive nesting":       strings.Replace(valid, `"resumeState"`, `"private":`+strings.Repeat("[", 10001)+"0"+strings.Repeat("]", 10001)+`,"resumeState"`, 1),
		"invalid utf8":            strings.Replace(valid, `"resumeState"`, string([]byte{'"', 'x', '"', ':', '"', 255, '"', ','})+`"resumeState"`, 1),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := readFrame(framedText(text))
			require.ErrorIs(t, err, errProtocol)
			assert.NotContains(t, err.Error(), "secret")
		})
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], maxFrameBytes+1)
	_, err := readFrame(bytes.NewReader(header[:]))
	require.ErrorIs(t, err, errProtocol)
	// Declared bytes beyond a valid JSON prefix must be present.
	binary.LittleEndian.PutUint32(header[:], uint32(len(valid)+10))
	_, err = readFrame(io.MultiReader(bytes.NewReader(header[:]), strings.NewReader(valid)))
	require.ErrorIs(t, err, errProtocol)
}
func TestProjectionRequiresKnownRuntimeAndResume(t *testing.T) {
	tests := []struct{ runtime, resume, want string }{
		{`{"type":"active","activeFlags":[]}`, "resumed", "working"},
		{`{"type":"active","activeFlags":["waitingOnUserInput"]}`, "resumed", "waiting"},
		{`{"type":"active","activeFlags":["waitingOnApproval","waitingOnUserInput"]}`, "resumed", "waiting"},
		{`{"type":"idle"}`, "resumed", "idle"},
		{`{"type":"active"}`, "resumed", "unknown"},
		{`{"type":"active","activeFlags":["futureFlag"]}`, "resumed", "unknown"},
		{`{"type":"idle","activeFlags":["waitingOnApproval"]}`, "resumed", "unknown"},
		{`{"type":"notLoaded"}`, "resumed", "unknown"},
		{`{"type":"systemError"}`, "resumed", "unknown"},
		{`{"type":"active","activeFlags":[]}`, "resuming", "unknown"},
	}
	for _, test := range tests {
		t.Run(test.runtime+test.resume, func(t *testing.T) {
			text := strings.Replace(snapshotText(test.runtime), `"resumed"`, `"`+test.resume+`"`, 1)
			f, err := readFrame(framedText(text))
			require.NoError(t, err)
			assert.Equal(t, test.want, f.Change.State.status())
		})
	}
}
func TestReadPatchSkipsContentAndFailsClosedWhenValuePrecedesStatusPath(t *testing.T) {
	text := `{"params":{"change":{"type":"patches","revision":4,"baseRevision":3,"patches":[
 {"op":"replace","path":["turns",0,"text"],"value":{"private":["never retain"]}},
 {"op":"replace","path":["threadRuntimeStatus"],"value":{"type":"active","activeFlags":["waitingOnUserInput"]}}
 ]}}}`
	f, err := readFrame(framedText(text))
	require.NoError(t, err)
	initial := projection{ID: testThread, Resume: "resumed", HasRuntime: true, Runtime: runtimeStatus{Kind: "idle"}}
	next, changed, err := applyPatches(initial, f.Change.Patches)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "waiting", next.status())
	encoded, _ := json.Marshal(f)
	assert.NotContains(t, string(encoded), "never retain")
	reordered := `{"params":{"change":{"patches":[{"value":{"type":"idle"},"op":"replace","path":["threadRuntimeStatus"]}]}}}`
	f, err = readFrame(framedText(reordered))
	require.NoError(t, err)
	_, _, err = applyPatches(initial, f.Change.Patches)
	require.ErrorIs(t, err, errProtocol)
}
func TestApplyPatchesTracksFlagTransitionsAndRejectsInvalidPaths(t *testing.T) {
	initial := projection{ID: testThread, Resume: "resumed", HasRuntime: true, Runtime: runtimeStatus{Kind: "active", HasFlags: true}}
	waiting, _, err := applyPatches(initial, []patch{{Op: "add", Path: []string{"threadRuntimeStatus", "activeFlags", "0"}, Text: "waitingOnApproval", ValueRead: true}})
	require.NoError(t, err)
	assert.Equal(t, "waiting", waiting.status())
	working, _, err := applyPatches(waiting, []patch{{Op: "remove", Path: []string{"threadRuntimeStatus", "activeFlags", "0"}}})
	require.NoError(t, err)
	assert.Equal(t, "working", working.status())
	assert.Empty(t, initial.Runtime.Flags)
	for _, p := range []patch{
		{Op: "replace", Path: []string{"threadRuntimeStatus", "activeFlags", "2"}, Text: "waitingOnApproval", ValueRead: true},
		{Op: "move", Path: []string{"resumeState"}},
		{Op: "replace", Path: []string{"id"}, Text: testOwner, ValueRead: true},
		{Op: "replace", Path: []string{"threadRuntimeStatus", "future"}, ValueRead: true},
	} {
		_, _, err := applyPatches(initial, []patch{p})
		require.ErrorIs(t, err, errProtocol)
	}
}
func TestAcceptStateRejectsOtherOwnersTargetsVersionsAndRevisionGaps(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	initial, err := readFrame(framedText(snapshotText(`{"type":"active","activeFlags":[]}`)))
	require.NoError(t, err)
	for _, name := range []string{"owner", "target", "version", "host", "thread id", "gap", "backwards"} {
		t.Run(name, func(t *testing.T) {
			state := &watchState{clientID: testClient, followed: map[string]*followed{testThread: {candidate: candidate{ID: testThread}, owner: testOwner, revision: -1}}}
			require.NoError(t, state.acceptState(initial, now))
			f := initial
			switch name {
			case "owner":
				f.SourceID = testClient
			case "target":
				f.Targets = []string{testOwner}
			case "version":
				f.Version = 12
			case "host":
				f.HostID = "remote"
			case "thread id":
				f.Change.State.ID = testOwner
			case "gap":
				f.Change = change{Kind: "patches", BaseRevision: 1, Revision: 2}
			case "backwards":
				f.Change.Revision = 2
			}
			require.ErrorIs(t, state.acceptState(f, now.Add(time.Second)), errProtocol)
			assert.False(t, state.followed[testThread].valid)
		})
	}
}
