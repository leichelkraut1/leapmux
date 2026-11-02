// Package codexobserve follows runtime status from an existing, verified Codex
// app service. It never starts a server, resumes a chat, or sends chat controls.
// Frames are capped at 64 MiB. The decoder may transiently buffer large string
// tokens, but conversation payloads are discarded rather than cached or returned.
package codexobserve

import (
	"encoding/binary"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

const maxFrameBytes = 64 << 20
const maxPatches = 8192

// Errors deliberately contain no decoder text, JSON pointer, path or payload.
var errProtocol = errors.New("unsupported native status protocol")

type runtimeStatus struct {
	Kind     string
	Flags    []string
	HasFlags bool
}
type projection struct {
	ID         string
	Resume     string
	Runtime    runtimeStatus
	HasRuntime bool
}
type patch struct {
	Op        string
	Path      []string
	ValueRead bool
	Text      string
	Runtime   runtimeStatus
	Flags     []string
	State     projection
}
type change struct {
	Kind         string
	Revision     int64
	BaseRevision int64
	State        projection
	Patches      []patch
}
type frame struct {
	Type, Method, RequestID, SourceID, HandledBy, ResultType, ClientID string
	Version                                                            int64
	Targets                                                            []string
	HostID, ConversationID, Status                                     string
	Change                                                             change
}

func readFrame(r io.Reader) (frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame{}, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > maxFrameBytes {
		return frame{}, errProtocol
	}
	if timed, ok := r.(interface{ SetReadDeadline(time.Time) error }); ok {
		if timed.SetReadDeadline(time.Now().Add(snapshotFor)) != nil {
			return frame{}, errProtocol
		}
	}
	limited := &io.LimitedReader{R: r, N: int64(size)}
	d := jsontext.NewDecoder(limited)
	f := frame{Version: -1, Change: change{Revision: -1, BaseRevision: -1}}
	err := readObject(d, func(key string) error {
		switch key {
		case "type":
			return readText(d, &f.Type)
		case "method":
			return readText(d, &f.Method)
		case "requestId":
			return readText(d, &f.RequestID)
		case "sourceClientId":
			return readText(d, &f.SourceID)
		case "handledByClientId":
			return readText(d, &f.HandledBy)
		case "resultType":
			return readText(d, &f.ResultType)
		case "version":
			return readInteger(d, &f.Version)
		case "targetClientIds":
			return readStrings(d, &f.Targets, 64)
		case "result":
			if d.PeekKind() != '{' {
				return d.SkipValue()
			}
			return readObject(d, func(key string) error {
				if key == "clientId" {
					return readText(d, &f.ClientID)
				}
				return d.SkipValue()
			})
		case "params":
			return readObject(d, func(key string) error {
				switch key {
				case "hostId":
					return readText(d, &f.HostID)
				case "conversationId":
					return readText(d, &f.ConversationID)
				case "clientId":
					return readText(d, &f.ClientID)
				case "status":
					return readText(d, &f.Status)
				case "change":
					return readChange(d, &f.Change)
				default:
					return d.SkipValue()
				}
			})
		default:
			return d.SkipValue()
		}
	})
	if err != nil {
		return frame{}, errProtocol
	}
	// A valid prefix is not evidence: validate the entire frame, including skipped
	// content, and reject a second value or a truncated declared frame.
	if _, err = d.ReadToken(); err != io.EOF || limited.N != 0 {
		return frame{}, errProtocol
	}
	return f, nil
}

func readChange(d *jsontext.Decoder, c *change) error {
	return readObject(d, func(key string) error {
		switch key {
		case "type":
			return readText(d, &c.Kind)
		case "revision":
			return readInteger(d, &c.Revision)
		case "baseRevision":
			return readInteger(d, &c.BaseRevision)
		case "conversationState":
			return readProjection(d, &c.State)
		case "patches":
			if err := tokenKind(d, '['); err != nil {
				return err
			}
			for d.PeekKind() != ']' {
				if len(c.Patches) >= maxPatches {
					return errProtocol
				}
				p := patch{}
				if err := readPatch(d, &p); err != nil {
					return err
				}
				c.Patches = append(c.Patches, p)
			}
			return tokenKind(d, ']')
		default:
			return d.SkipValue()
		}
	})
}
func readProjection(d *jsontext.Decoder, p *projection) error {
	return readObject(d, func(key string) error {
		switch key {
		case "id":
			return readText(d, &p.ID)
		case "resumeState":
			return readNullableText(d, &p.Resume)
		case "threadRuntimeStatus":
			if d.PeekKind() == 'n' {
				return tokenKind(d, 'n')
			}
			p.HasRuntime = true
			return readRuntime(d, &p.Runtime)
		default:
			return d.SkipValue()
		}
	})
}
func readRuntime(d *jsontext.Decoder, r *runtimeStatus) error {
	return readObject(d, func(key string) error {
		switch key {
		case "type":
			return readText(d, &r.Kind)
		case "activeFlags":
			r.HasFlags = true
			return readStrings(d, &r.Flags, 8)
		default:
			return d.SkipValue()
		}
	})
}
func readPatch(d *jsontext.Decoder, p *patch) error {
	pathRead := false
	return readObject(d, func(key string) error {
		switch key {
		case "op":
			return readText(d, &p.Op)
		case "path":
			pathRead = true
			if err := tokenKind(d, '['); err != nil {
				return err
			}
			for d.PeekKind() != ']' {
				if len(p.Path) >= 64 {
					return errProtocol
				}
				var part string
				if d.PeekKind() == '0' {
					var index int64
					if err := readInteger(d, &index); err != nil {
						return err
					}
					part = strconv.FormatInt(index, 10)
				} else if err := readText(d, &part); err != nil {
					return err
				}
				p.Path = append(p.Path, part)
			}
			return tokenKind(d, ']')
		case "value":
			// Immer emits op/path/value. A reordered value before its path is skipped;
			// if the eventual path affects status, applying the patch fails closed.
			if !pathRead {
				return d.SkipValue()
			}
			if !statusPath(p.Path) {
				return d.SkipValue()
			}
			p.ValueRead = true
			switch {
			case len(p.Path) == 0:
				return readProjection(d, &p.State)
			case len(p.Path) == 1 && p.Path[0] == "threadRuntimeStatus":
				if d.PeekKind() == 'n' {
					p.ValueRead = false
					return tokenKind(d, 'n')
				}
				return readRuntime(d, &p.Runtime)
			case len(p.Path) == 2 && p.Path[0] == "threadRuntimeStatus" && p.Path[1] == "activeFlags":
				return readStrings(d, &p.Flags, 8)
			default:
				return readNullableText(d, &p.Text)
			}
		default:
			return d.SkipValue()
		}
	})
}
func statusPath(path []string) bool {
	return len(path) == 0 || path[0] == "threadRuntimeStatus" || path[0] == "resumeState" || path[0] == "id"
}
func applyPatches(state projection, patches []patch) (projection, bool, error) {
	changed := false
	state.Runtime.Flags = append([]string(nil), state.Runtime.Flags...)
	for _, p := range patches {
		if !statusPath(p.Path) {
			continue
		}
		changed = true
		if p.Op != "add" && p.Op != "replace" && p.Op != "remove" {
			return projection{}, false, errProtocol
		}
		if p.Op != "remove" && !p.ValueRead {
			return projection{}, false, errProtocol
		}
		if len(p.Path) == 0 {
			if p.Op == "remove" {
				return projection{}, false, errProtocol
			}
			state = p.State
			continue
		}
		switch p.Path[0] {
		case "id":
			if len(p.Path) != 1 || p.Op == "remove" || state.ID != p.Text {
				return projection{}, false, errProtocol
			}
		case "resumeState":
			if len(p.Path) != 1 {
				return projection{}, false, errProtocol
			}
			state.Resume = p.Text
		case "threadRuntimeStatus":
			if len(p.Path) == 1 {
				state.Runtime = p.Runtime
				state.HasRuntime = p.Op != "remove"
			} else if len(p.Path) == 2 && p.Path[1] == "type" {
				state.Runtime.Kind = p.Text
			} else if len(p.Path) == 2 && p.Path[1] == "activeFlags" {
				state.Runtime.Flags = append([]string(nil), p.Flags...)
				state.Runtime.HasFlags = p.Op != "remove"
			} else if len(p.Path) == 3 && p.Path[1] == "activeFlags" {
				index, err := strconv.Atoi(p.Path[2])
				if err != nil || index < 0 || index > len(state.Runtime.Flags) {
					return projection{}, false, errProtocol
				}
				flags := state.Runtime.Flags
				switch p.Op {
				case "add":
					flags = append(flags, "")
					copy(flags[index+1:], flags[index:])
					flags[index] = p.Text
				case "replace":
					if index == len(flags) {
						return projection{}, false, errProtocol
					}
					flags[index] = p.Text
				case "remove":
					if index == len(flags) {
						return projection{}, false, errProtocol
					}
					flags = append(flags[:index], flags[index+1:]...)
				}
				state.Runtime.Flags = flags
			} else {
				return projection{}, false, errProtocol
			}
		}
	}
	return state, changed, nil
}

func (p projection) status() string {
	if !p.HasRuntime || p.Resume != "resumed" {
		return "unknown"
	}
	r := p.Runtime
	for _, flag := range r.Flags {
		if flag != "waitingOnApproval" && flag != "waitingOnUserInput" {
			return "unknown"
		}
	}
	switch r.Kind {
	case "active":
		if !r.HasFlags {
			return "unknown"
		}
		if len(r.Flags) > 0 {
			return "waiting"
		}
		return "working"
	case "idle":
		if len(r.Flags) == 0 {
			return "idle"
		}
	}
	return "unknown"
}

func readObject(d *jsontext.Decoder, member func(string) error) error {
	if err := tokenKind(d, '{'); err != nil {
		return err
	}
	for d.PeekKind() != '}' {
		var key string
		if err := readText(d, &key); err != nil {
			return err
		}
		if err := member(key); err != nil {
			return err
		}
	}
	return tokenKind(d, '}')
}
func readText(d *jsontext.Decoder, dst *string) error {
	token, err := d.ReadToken()
	if err != nil || token.Kind() != '"' {
		return errProtocol
	}
	value := token.String()
	if len(value) > 1024 {
		return errProtocol
	}
	*dst = strings.Clone(value)
	return nil
}
func readNullableText(d *jsontext.Decoder, dst *string) error {
	if d.PeekKind() == 'n' {
		*dst = ""
		return tokenKind(d, 'n')
	}
	return readText(d, dst)
}
func readInteger(d *jsontext.Decoder, dst *int64) error {
	token, err := d.ReadToken()
	if err != nil || token.Kind() != '0' {
		return errProtocol
	}
	value, err := token.Int()
	if err != nil || value < 0 || value > 9007199254740991 {
		return errProtocol
	}
	*dst = value
	return nil
}
func readStrings(d *jsontext.Decoder, dst *[]string, limit int) error {
	if err := tokenKind(d, '['); err != nil {
		return err
	}
	for d.PeekKind() != ']' {
		if len(*dst) >= limit {
			return errProtocol
		}
		var value string
		if err := readText(d, &value); err != nil {
			return err
		}
		*dst = append(*dst, value)
	}
	return tokenKind(d, ']')
}
func tokenKind(d *jsontext.Decoder, kind jsontext.Kind) error {
	token, err := d.ReadToken()
	if err != nil || token.Kind() != kind {
		return errProtocol
	}
	return nil
}

func writeFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 8192 {
		return errProtocol
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(data)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}
func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
