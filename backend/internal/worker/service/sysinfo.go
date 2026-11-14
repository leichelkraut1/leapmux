package service

import (
	"context"
	"os"
	"runtime"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/version"
)

func registerSysInfoHandlers(d ownerOnlyRegistrar, svc *Service) {
	d.Register("GetWorkerSystemInfo", leapmuxv1.Scope_SCOPE_WORKER_READ, func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		homeDir, _ := os.UserHomeDir()
		sendProtoResponse(sender, &leapmuxv1.GetWorkerSystemInfoResponse{
			Name:         svc.Name,
			Os:           runtime.GOOS,
			Arch:         runtime.GOARCH,
			HomeDir:      homeDir,
			Version:      version.Value,
			CommitHash:   version.CommitHash,
			BuildTime:    version.BuildTime,
			Branch:       version.Branch,
			Capabilities: systemCapabilities(),
		})
	})
}

// tabCapabilityPrefix marks a tab kind this build can hold, as
// "tab:<wire token>" (contracts/tab-types.json), so the browser opens such a
// tab only on a computer whose worker will keep it. Without it an older
// worker refuses the tab's payload and its report drops the tab, and the tab
// would vanish the moment it opened.
const tabCapabilityPrefix = "tab:"

// systemCapabilities is what GetWorkerSystemInfo reports: workflow
// placement's capabilities plus the tab kinds only some builds can hold.
func systemCapabilities() []string {
	caps := slices.Clone(workerCapabilities())
	caps = append(caps, tabCapabilityPrefix+contracts.TabTypeWireToken[leapmuxv1.TabType_TAB_TYPE_TRADING_RESEARCH])
	slices.Sort(caps)
	return caps
}
