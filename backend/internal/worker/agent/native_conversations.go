package agent

import "time"

// NativeConversationQuery names an existing native session, never a path or
// process to start. Cursors are opaque provider-owned read positions.
type NativeConversationQuery struct {
	SessionID string
	Cursor    string
	Direction string
	Limit     int
}

// NativeConversationRecord retains one complete unchanged native JSON envelope.
// Seq is its source byte offset plus one. It is not a managed transcript seq.
type NativeConversationRecord struct {
	ID         string
	Seq        int64
	NativeJSON []byte
}

// NativeConversationPage is an ephemeral read-only view of external history.
// Append-only writes retain Revision. A replacement or observed truncation
// invalidates cursors and sets ResetRequired rather than mixing generations.
type NativeConversationPage struct {
	Format        string
	SessionID     string
	Revision      string
	DisplayName   string
	WorkingDir    string
	OlderCursor   string
	NewerCursor   string
	ObservedAt    time.Time
	Records       []NativeConversationRecord
	HasOlder      bool
	HasNewer      bool
	ResetRequired bool
	PartialReason string
}
