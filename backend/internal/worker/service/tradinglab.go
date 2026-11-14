package service

import (
	"context"
	"errors"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/tradinglab"
)

// Trading lab methods (proto/leapmux/v1/trading_lab.proto) follow the
// workflow methods' scopes: reads under agent:read, changes under
// agent:write, owner-only. A research agent never reaches them, because its
// socket is confined to the workflow participant methods. Approving a live
// order or an execution policy, switching to live mode, setting the connector
// passphrase and signing in to the broker are the person's own acts: an
// unconfined agent, an API token or an app holds the owner's identity too, so
// they need byPerson, a call from the person's browser session.
func registerTradingLabHandlers(r registrar, svc *Service) {
	const read, write = leapmuxv1.Scope_SCOPE_AGENT_READ, leapmuxv1.Scope_SCOPE_AGENT_WRITE
	lab := svc.TradingLab
	// byPerson: the Hub said a browser session opened the channel, and the
	// call did not come through a local control socket.
	byPerson := func(ctx context.Context, caller channel.Caller) bool {
		return caller.BrowserSession && !channel.ViaAgentSocket(ctx)
	}

	registerOwnerGuarded(r, "GetTradingLabSettings", read, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.GetTradingLabSettingsRequest, sender channel.ResponseWriter) {
			s, err := lab.Settings(ctx, caller.UserID.String())
			replyLab(sender, &leapmuxv1.GetTradingLabSettingsResponse{Settings: s}, err)
		})
	registerOwnerGuarded(r, "SaveTradingLabSettings", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.SaveTradingLabSettingsRequest, sender channel.ResponseWriter) {
			if !byPerson(ctx, caller) && (req.GetNewConnectorPassphrase() != "" ||
				req.GetSettings().GetMode() == leapmuxv1.TradingLabMode_TRADING_LAB_MODE_LIVE) {
				sendPermissionDenied(sender, "only you, in your browser, can set the connector passphrase or keep the lab in live trading")
				return
			}
			s, err := lab.SaveSettings(ctx, caller.UserID.String(), req)
			replyLab(sender, &leapmuxv1.SaveTradingLabSettingsResponse{Settings: s}, err)
		})
	registerOwnerGuarded(r, "VerifyTradingBroker", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.VerifyTradingBrokerRequest, sender channel.ResponseWriter) {
			st, err := lab.VerifyBroker(ctx, caller.UserID.String())
			replyLab(sender, &leapmuxv1.VerifyTradingBrokerResponse{Status: st}, err)
		})
	registerOwnerGuarded(r, "InstallTradingSampleData", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.InstallTradingSampleDataRequest, sender channel.ResponseWriter) {
			resp, err := lab.InstallSampleData(ctx, caller.UserID.String(), svc.DataDir)
			replyLab(sender, resp, err)
		})
	registerOwnerGuarded(r, "GetTradingLabRun", read, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.GetTradingLabRunRequest, sender channel.ResponseWriter) {
			v, err := lab.RunView(ctx, caller.UserID.String(), req.GetRunId())
			replyLab(sender, &leapmuxv1.GetTradingLabRunResponse{View: v}, err)
		})
	registerOwnerGuarded(r, "DecideTradingCandidate", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.DecideTradingCandidateRequest, sender channel.ResponseWriter) {
			c, err := lab.DecideCandidate(ctx, caller.UserID.String(), req)
			replyLab(sender, &leapmuxv1.DecideTradingCandidateResponse{Candidate: c}, err)
		})
	registerOwnerGuarded(r, "GetTradingDashboard", read, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.GetTradingDashboardRequest, sender channel.ResponseWriter) {
			resp, err := lab.Dashboard(ctx, caller.UserID.String())
			replyLab(sender, resp, err)
		})
	registerOwnerGuarded(r, "MarkTradingBooks", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.MarkTradingBooksRequest, sender channel.ResponseWriter) {
			resp, err := lab.MarkBooks(ctx, caller.UserID.String())
			replyLab(sender, resp, err)
		})
	registerOwnerGuarded(r, "PrepareTradingOrder", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.PrepareTradingOrderRequest, sender channel.ResponseWriter) {
			o, err := lab.Prepare(ctx, caller.UserID.String(), req)
			replyLab(sender, &leapmuxv1.PrepareTradingOrderResponse{Order: o}, err)
		})
	registerOwnerGuarded(r, "ConfirmTradingOrder", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.ConfirmTradingOrderRequest, sender channel.ResponseWriter) {
			o, err := lab.Confirm(ctx, caller.UserID.String(), req, byPerson(ctx, caller))
			replyLab(sender, &leapmuxv1.ConfirmTradingOrderResponse{Order: o}, err)
		})
	registerOwnerGuarded(r, "CancelTradingOrder", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.CancelTradingOrderRequest, sender channel.ResponseWriter) {
			o, err := lab.Cancel(ctx, caller.UserID.String(), req.GetOrderId())
			replyLab(sender, &leapmuxv1.CancelTradingOrderResponse{Order: o}, err)
		})
	registerOwnerGuarded(r, "ReconcileTradingOrders", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.ReconcileTradingOrdersRequest, sender channel.ResponseWriter) {
			orders, err := lab.Reconcile(ctx, caller.UserID.String())
			replyLab(sender, &leapmuxv1.ReconcileTradingOrdersResponse{Orders: orders}, err)
		})
	registerOwnerGuarded(r, "SaveTradingExecutionPolicy", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.SaveTradingExecutionPolicyRequest, sender channel.ResponseWriter) {
			p, err := lab.SavePolicy(ctx, caller.UserID.String(), req, byPerson(ctx, caller))
			replyLab(sender, &leapmuxv1.SaveTradingExecutionPolicyResponse{Policy: p}, err)
		})
	registerOwnerGuarded(r, "StartTradingBrokerSignIn", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.StartTradingBrokerSignInRequest, sender channel.ResponseWriter) {
			if !byPerson(ctx, caller) {
				sendPermissionDenied(sender, "only you, in your browser, can sign in to the broker")
				return
			}
			u, err := lab.StartBrokerSignIn(ctx, caller.UserID.String())
			replyLab(sender, &leapmuxv1.StartTradingBrokerSignInResponse{AuthorizeUrl: u}, err)
		})
	registerOwnerGuarded(r, "SignOutTradingBroker", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.SignOutTradingBrokerRequest, sender channel.ResponseWriter) {
			s, err := lab.SignOutBroker(ctx, caller.UserID.String())
			replyLab(sender, &leapmuxv1.SignOutTradingBrokerResponse{Settings: s}, err)
		})
	registerOwnerGuarded(r, "RevokeTradingExecutionPolicy", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.RevokeTradingExecutionPolicyRequest, sender channel.ResponseWriter) {
			p, err := lab.RevokePolicy(ctx, caller.UserID.String(), req.GetPolicyId())
			replyLab(sender, &leapmuxv1.RevokeTradingExecutionPolicyResponse{Policy: p}, err)
		})
}

// replyLab answers with resp, or maps a lab error to its code.
func replyLab(sender channel.ResponseWriter, resp proto.Message, err error) {
	switch {
	case err == nil:
		sendProtoResponse(sender, resp)
	case errors.Is(err, tradinglab.ErrInvalid):
		sendInvalidArgument(sender, err.Error())
	case errors.Is(err, tradinglab.ErrNotFound):
		sendNotFoundError(sender, "not found")
	case errors.Is(err, tradinglab.ErrConflict):
		sendFailedPrecondition(sender, err.Error())
	case errors.Is(err, tradinglab.ErrDenied):
		sendPermissionDenied(sender, err.Error())
	default:
		sendInternalError(sender, "trading lab: "+err.Error())
	}
}
