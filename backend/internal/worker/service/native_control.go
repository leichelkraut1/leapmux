package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/grpc/codes"
)

var nativeControlRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
var nativeControlBinding = regexp.MustCompile(`^[A-Za-z0-9_.-]{16,256}$`)
var nativeControlTurnID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func registerNativeControlHandlers(d registrar, svc *Service) {
	registerOwnerGuarded(d, "InspectNativeSessionControl", leapmuxv1.Scope_SCOPE_AGENT_READ, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, req *leapmuxv1.InspectNativeSessionControlRequest, sender channel.ResponseWriter) {
			if !nativeSessionID.MatchString(req.SessionId) {
				sendInvalidArgument(sender, "invalid native chat identity")
				return
			}
			ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			state := agent.NativeSessionControlState{SessionID: req.SessionId}
			external, err := svc.nativeControlIsExternal(ctx, req.AgentProvider, req.SessionId)
			if err == nil && !external {
				state.UnavailableReason = "Open this LeapMux-managed chat from its workspace to send messages."
			} else if err == nil {
				state, err = svc.Agents.InspectNativeSessionControl(ctx, req.AgentProvider, req.SessionId)
			}
			if errors.Is(err, agent.ErrNativeControlUnavailable) {
				if state.SessionID != req.SessionId {
					state = agent.NativeSessionControlState{SessionID: req.SessionId}
				}
				// A lost owner may return authoritative pending-input outcomes.
				// Preserve those bounded results, but never issue a live lease.
				state.CanSend, state.CanInterrupt = false, false
				state.BindingToken, state.ActiveTurnID = "", ""
				state.ExpiresAt = time.Time{}
				state.ActivityState = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
				state.UnavailableReason = "This chat has no available connection to its original app."
			} else if err != nil {
				sendNativeControlError(sender, err)
				return
			}
			response, err := nativeControlResponse(req.SessionId, state, time.Now())
			if err != nil {
				sendInternalError(sender, "native chat returned an invalid control state")
				return
			}
			sendProtoResponse(sender, response)
		})
	registerOwnerGuarded(d, "SendNativeSessionInput", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, req *leapmuxv1.SendNativeSessionInputRequest, sender channel.ResponseWriter) {
			if !validNativeControlMutation(req.SessionId, req.BindingToken, req.RequestId) || !validNativeControlText(req.Text) || !validNativeInputBehavior(req.InputBehavior) {
				sendInvalidArgument(sender, "invalid native chat message")
				return
			}
			ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			releaseNative, err := svc.nativeControlAdmissions.acquire(ctx, req.AgentProvider, req.SessionId)
			if err != nil {
				sendNativeControlError(sender, err)
				return
			}
			defer releaseNative()
			if !svc.requireExternalNativeControl(ctx, sender, req.AgentProvider, req.SessionId) {
				return
			}
			receipt, err := svc.Agents.SendNativeSessionInput(ctx, req.AgentProvider, agent.NativeSessionInput{
				SessionID: req.SessionId, BindingToken: req.BindingToken, RequestID: req.RequestId, Text: req.Text, Behavior: req.InputBehavior,
			})
			if err != nil {
				sendNativeControlError(sender, err)
				return
			}
			if (receipt.Disposition != leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_ACCEPTED &&
				receipt.Disposition != leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_QUEUED) || len(receipt.Detail) > 1024 {
				sendNativeControlError(sender, agent.ErrNativeControlUncertain)
				return
			}
			sendProtoResponse(sender, &leapmuxv1.SendNativeSessionInputResponse{
				SessionId: req.SessionId, RequestId: req.RequestId, Disposition: receipt.Disposition, Detail: receipt.Detail,
			})
		})
	registerOwnerGuarded(d, "InterruptNativeSession", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, req *leapmuxv1.InterruptNativeSessionRequest, sender channel.ResponseWriter) {
			if !validNativeControlMutation(req.SessionId, req.BindingToken, req.RequestId) || !nativeControlTurnID.MatchString(req.ExpectedTurnId) {
				sendInvalidArgument(sender, "invalid native chat interruption")
				return
			}
			ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			releaseNative, err := svc.nativeControlAdmissions.acquire(ctx, req.AgentProvider, req.SessionId)
			if err != nil {
				sendNativeControlError(sender, err)
				return
			}
			defer releaseNative()
			if !svc.requireExternalNativeControl(ctx, sender, req.AgentProvider, req.SessionId) {
				return
			}
			err = svc.Agents.InterruptNativeSession(ctx, req.AgentProvider, agent.NativeSessionInterrupt{
				SessionID: req.SessionId, BindingToken: req.BindingToken, RequestID: req.RequestId, ExpectedTurnID: req.ExpectedTurnId,
			})
			if err != nil {
				sendNativeControlError(sender, err)
				return
			}
			sendProtoResponse(sender, &leapmuxv1.InterruptNativeSessionResponse{SessionId: req.SessionId, RequestId: req.RequestId, Detail: "Interruption accepted by the original app."})
		})
}

func validNativeControlMutation(session, binding, request string) bool {
	return nativeSessionID.MatchString(session) && nativeControlBinding.MatchString(binding) && nativeControlRequestID.MatchString(request)
}

func validNativeControlText(text string) bool {
	return len(text) <= contracts.NativeSessionControlMaxTextBytes && utf8.ValidString(text) &&
		strings.TrimSpace(text) != "" && !strings.ContainsRune(text, '\x00')
}

func validNativeInputBehavior(behavior leapmuxv1.NativeSessionInputBehavior) bool {
	return behavior == leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_QUEUE_NEXT_TURN ||
		behavior == leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_SEND_NOW
}

func nativeControlResponse(session string, state agent.NativeSessionControlState, now time.Time) (*leapmuxv1.InspectNativeSessionControlResponse, error) {
	if state.SessionID != session || len(state.OwnerLabel) > 256 || len(state.TransportLabel) > 256 || len(state.UnavailableReason) > 1024 || len(state.RecentInputs) > 20 {
		return nil, agent.ErrNativeControlInvalid
	}
	if state.ActiveTurnID != "" && !nativeControlTurnID.MatchString(state.ActiveTurnID) {
		return nil, agent.ErrNativeControlInvalid
	}
	if state.BindingToken != "" && !nativeControlBinding.MatchString(state.BindingToken) {
		return nil, agent.ErrNativeControlInvalid
	}
	if state.CanSend || state.CanInterrupt {
		if !nativeControlBinding.MatchString(state.BindingToken) || !agent.ValidNativeControlLease(state, state.BindingToken, now) ||
			state.ExpiresAt.After(now.Add(contracts.NativeSessionControlLeaseSeconds*time.Second+5*time.Second)) ||
			(state.CanInterrupt && state.ActiveTurnID == "") {
			return nil, agent.ErrNativeControlInvalid
		}
	}
	if state.CanSend && !validNativeInputBehavior(state.InputBehavior) {
		return nil, agent.ErrNativeControlInvalid
	}
	response := &leapmuxv1.InspectNativeSessionControlResponse{
		SessionId: session, OwnerLabel: normalizeNativeSessionDisplayName(state.OwnerLabel),
		TransportLabel: normalizeNativeSessionDisplayName(state.TransportLabel),
		CanSend:        state.CanSend, CanInterrupt: state.CanInterrupt, UnavailableReason: state.UnavailableReason,
		BindingToken: state.BindingToken, ActiveTurnId: state.ActiveTurnID, ActivityState: state.ActivityState,
		InputBehavior: state.InputBehavior,
	}
	if !state.ObservedAt.IsZero() {
		response.ObservedAt = timefmt.Format(state.ObservedAt)
	}
	if !state.ExpiresAt.IsZero() {
		response.ExpiresAt = timefmt.Format(state.ExpiresAt)
	}
	seen := make(map[string]bool, len(state.RecentInputs))
	for _, input := range state.RecentInputs {
		if !nativeControlRequestID.MatchString(input.RequestID) || len(input.Detail) > 1024 ||
			seen[input.RequestID] ||
			input.State < leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED ||
			input.State > leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN ||
			(input.TurnID != "" && !nativeControlTurnID.MatchString(input.TurnID)) {
			return nil, agent.ErrNativeControlInvalid
		}
		seen[input.RequestID] = true
		response.RecentInputs = append(response.RecentInputs, &leapmuxv1.NativeSessionInputOutcome{
			RequestId: input.RequestID, State: input.State, Detail: input.Detail, TurnId: input.TurnID,
		})
	}
	return response, nil
}

// An external viewer cannot bypass the control path of a live LeapMux agent.
// This reads identities only; it never reads messages or interprets provider data.
func (svc *Service) nativeControlIsExternal(ctx context.Context, provider leapmuxv1.AgentProvider, session string) (bool, error) {
	if svc.Queries == nil {
		return false, agent.ErrNativeControlUnavailable
	}
	// Durable open/pending root claims exist before a startup enters Manager.
	// Mutations hold the same native-identity permit as resume claim creation.
	open, err := svc.Queries.HasOpenAgentForNativeSession(ctx, db.HasOpenAgentForNativeSessionParams{
		AgentProvider: provider, SessionID: session,
	})
	if err != nil {
		return false, agent.ErrNativeControlUnavailable
	}
	if open {
		return false, nil
	}
	owners := svc.Agents.ListAgentIDs()
	if len(owners) > maxNativeManagedIdentities || (len(owners) != 0 && svc.Queries == nil) {
		return false, agent.ErrNativeControlUnavailable
	}
	count := 0
	for _, owner := range owners {
		rows, err := svc.Queries.ListAgentTreeNativeSessionIdentities(ctx, owner)
		if err != nil {
			return false, agent.ErrNativeControlUnavailable
		}
		count += len(rows)
		if count > maxNativeManagedIdentities {
			return false, agent.ErrNativeControlUnavailable
		}
		for _, row := range rows {
			if row.AgentProvider == provider && (row.AgentSessionID == session || row.PendingResumeSessionID == session) {
				return false, nil
			}
		}
	}
	return true, nil
}

func (svc *Service) requireExternalNativeControl(ctx context.Context, sender channel.ResponseWriter, provider leapmuxv1.AgentProvider, session string) bool {
	external, err := svc.nativeControlIsExternal(ctx, provider, session)
	if err != nil {
		sendNativeControlError(sender, err)
		return false
	}
	if !external {
		sendFailedPrecondition(sender, "use the workspace controls for this LeapMux-managed chat")
		return false
	}
	return true
}

func sendNativeControlError(sender channel.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrNativeControlInvalid):
		sendInvalidArgument(sender, "invalid native chat control request")
	case errors.Is(err, agent.ErrNativeControlStale):
		sendFailedPrecondition(sender, "the chat connection or active turn changed; refresh before trying again")
	case errors.Is(err, agent.ErrNativeControlUnavailable):
		sendFailedPrecondition(sender, "the original app is not available for this operation")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, agent.ErrNativeControlUncertain):
		_ = sender.SendError(int32(codes.Aborted), "delivery is unconfirmed; check the original chat before sending again")
	default:
		sendInternalError(sender, "the original app could not confirm this operation")
	}
}
