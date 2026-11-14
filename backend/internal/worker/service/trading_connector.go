package service

import (
	"context"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/labmcp"
	"github.com/leapmux/leapmux/util/version"
)

// RunTradingConnector serves the trading lab's read-only Claude connector
// when TradingConnectorListen is set, until ctx ends. It answers for the
// worker's owner only, and a failure to start leaves the worker running.
func (svc *Service) RunTradingConnector(ctx context.Context) {
	if svc.Config.TradingConnectorListen == "" {
		return
	}
	srv, err := labmcp.New(labmcp.Config{
		DB: svc.Config.DB, Lab: svc.TradingLab, Runs: svc.Workflows,
		Owner:     func() string { return svc.RegisteredBy().String() },
		PublicURL: svc.Config.TradingConnectorURL, Version: version.Value, Clock: svc.Config.Clock,
	})
	if err != nil {
		slog.Error("trading connector not started", "error", err)
		return
	}
	if err := srv.Serve(ctx, svc.Config.TradingConnectorListen); err != nil {
		slog.Error("trading connector stopped", "error", err)
	}
}
