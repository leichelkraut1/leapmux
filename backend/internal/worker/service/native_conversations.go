package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

const maxNativeConversationRecords = 200
const maxNativeConversationBytes = 4 << 20

// History is an explicit, separately authorized read. Inventory polling never
// invokes this handler or includes conversation contents in its status replies.
func registerNativeConversationHandlers(d registrar, svc *Service) {
	registerOwnerGuarded(d, "ReadNativeConversation", leapmuxv1.Scope_SCOPE_AGENT_READ, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, req *leapmuxv1.ReadNativeConversationRequest, sender channel.ResponseWriter) {
			query, err := nativeConversationQuery(req)
			if err != nil {
				sendInvalidArgument(sender, "invalid native conversation request")
				return
			}
			reg, ok := svc.Agents.Registry().Registration(req.AgentProvider)
			if !ok || reg.ReadNativeConversation == nil {
				sendNotFoundError(sender, "native conversation viewing is unavailable for this provider")
				return
			}
			ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			page, err := reg.ReadNativeConversation(ctx, query)
			if err != nil {
				// Provider errors may hold local paths or native source data. Keep
				// those off the wire and out of application logs.
				if errors.Is(err, os.ErrNotExist) {
					sendNotFoundError(sender, "saved conversation is not available on this computer")
				} else {
					sendInternalError(sender, "saved conversation could not be read safely")
				}
				return
			}
			response, err := nativeConversationResponse(query.SessionID, page)
			if err != nil {
				sendInternalError(sender, "saved conversation returned an invalid page")
				return
			}
			sendProtoResponse(sender, response)
		})
}

func nativeConversationQuery(req *leapmuxv1.ReadNativeConversationRequest) (agent.NativeConversationQuery, error) {
	if req == nil || !nativeSessionID.MatchString(req.SessionId) || len(req.Cursor) > 2048 || req.Limit < 0 || req.Limit > maxNativeConversationRecords {
		return agent.NativeConversationQuery{}, errors.New("invalid request")
	}
	direction := req.Direction
	if direction == "" {
		direction = contracts.NativeConversationDirectionLatest
	}
	if direction != contracts.NativeConversationDirectionLatest && direction != contracts.NativeConversationDirectionBefore && direction != contracts.NativeConversationDirectionAfter {
		return agent.NativeConversationQuery{}, errors.New("invalid direction")
	}
	if (direction == contracts.NativeConversationDirectionLatest) != (req.Cursor == "") {
		return agent.NativeConversationQuery{}, errors.New("invalid cursor")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 100
	}
	return agent.NativeConversationQuery{SessionID: req.SessionId, Cursor: req.Cursor, Direction: direction, Limit: limit}, nil
}

func nativeConversationResponse(sessionID string, page agent.NativeConversationPage) (*leapmuxv1.ReadNativeConversationResponse, error) {
	if page.SessionID != sessionID || len(page.Format) > 128 || len(page.Revision) > 256 || len(page.WorkingDir) > 32768 ||
		len(page.Records) > maxNativeConversationRecords || len(page.OlderCursor) > 2048 || len(page.NewerCursor) > 2048 || len(page.PartialReason) > 1024 ||
		(page.HasOlder && page.OlderCursor == "") || (page.HasNewer && page.NewerCursor == "") {
		return nil, errors.New("invalid page")
	}
	response := &leapmuxv1.ReadNativeConversationResponse{
		Format: page.Format, SessionId: page.SessionID, Revision: page.Revision,
		DisplayName: normalizeNativeSessionDisplayName(page.DisplayName), WorkingDir: page.WorkingDir,
		OlderCursor: page.OlderCursor, NewerCursor: page.NewerCursor,
		ObservedAt: timefmt.Format(page.ObservedAt), HasOlder: page.HasOlder, HasNewer: page.HasNewer,
		ResetRequired: page.ResetRequired, PartialReason: page.PartialReason,
	}
	bytes := 0
	lastSeq := int64(0)
	ids := make(map[string]bool, len(page.Records))
	for _, record := range page.Records {
		bytes += len(record.NativeJSON)
		if bytes > maxNativeConversationBytes || record.ID == "" || len(record.ID) > 256 || ids[record.ID] || record.Seq <= lastSeq || !json.Valid(record.NativeJSON) {
			return nil, errors.New("invalid record")
		}
		ids[record.ID] = true
		lastSeq = record.Seq
		response.Records = append(response.Records, &leapmuxv1.NativeConversationRecord{Id: record.ID, Seq: record.Seq, NativeJson: record.NativeJSON})
	}
	return response, nil
}
