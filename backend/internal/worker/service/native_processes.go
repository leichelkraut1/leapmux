package service

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/procutil"
)

const nativeProcessScanTimeout = 8 * time.Second
const maxNativeProcesses = 64

type nativeProcessSource interface {
	Records() []procutil.ProcessRecord
	ShortName(context.Context, int) (string, error)
	UserKey(context.Context, int) (string, error)
	Executable(context.Context, int) (string, error)
	ObservationIdentity(context.Context, int) (procutil.ProcessIdentity, error)
	DescendantPIDs(int, int, int, func(int) bool) ([]int, int)
}

func registerNativeProcessHandlers(d registrar, svc *Service) {
	registerOwnerGuarded(d, "InspectNativeAgentProcesses", leapmuxv1.Scope_SCOPE_AGENT_READ, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, _ *leapmuxv1.InspectNativeAgentProcessesRequest, sender channel.ResponseWriter) {
			requestCtx := ctx
			// Start lazy observers early; this call returns cached metadata immediately.
			_ = svc.Agents.InspectNativeServices(requestCtx)
			ctx, cancel := context.WithTimeout(ctx, nativeProcessScanTimeout)
			defer cancel()
			table, err := procutil.SnapshotProcessTable(ctx)
			if err != nil || table == nil {
				sendInternalError(sender, "native process inventory is unavailable")
				return
			}
			result, identities, err := inspectNativeAgentProcesses(ctx, table, os.Getpid(), svc.Agents.Registry(), maxNativeProcesses)
			if err != nil {
				// Do not leak OS errors, executable paths, or other users' identities.
				sendInternalError(sender, "native process inventory is unavailable")
				return
			}
			observeNativeSessions(ctx, table, result, identities, svc.Agents.Registry())
			applyNativeServices(result, svc.Agents.InspectNativeServices(requestCtx), time.Now())
			excludeManagedNativeServiceSessions(requestCtx, svc.Queries, svc.Agents.ListAgentIDs, result)
			result.ObservedAt = timefmt.Format(time.Now())
			sendProtoResponse(sender, result)
		})
}

// inspectNativeAgentProcesses observes names only until a provider recognizes a
// candidate. It verifies the OS owner before reading its executable path, and
// excludes this Worker's entire process tree (including managed terminals).
// No command line, environment, transcript, lock or credential is read.
func inspectNativeAgentProcesses(ctx context.Context, source nativeProcessSource, workerPID int, registry *agent.Registry, limit int) (*leapmuxv1.InspectNativeAgentProcessesResponse, map[int]procutil.ProcessIdentity, error) {
	owner, err := source.UserKey(ctx, workerPID)
	if err != nil || strings.TrimSpace(owner) == "" {
		return nil, nil, errors.New("cannot establish process owner")
	}
	excluded := map[int]bool{workerPID: true}
	descendants, _ := source.DescendantPIDs(workerPID, 0, 0, nil)
	for _, pid := range descendants {
		excluded[pid] = true
	}
	records := source.Records()
	slices.SortFunc(records, func(a, b procutil.ProcessRecord) int { return a.PID - b.PID })
	result := &leapmuxv1.InspectNativeAgentProcessesResponse{}
	identities := make(map[int]procutil.ProcessIdentity)
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if excluded[record.PID] {
			continue
		}
		identity, identityErr := source.ObservationIdentity(ctx, record.PID)
		name, err := source.ShortName(ctx, record.PID)
		if err != nil {
			return nil, nil, errors.New("cannot read process name metadata")
		}
		provider, _ := registry.RecognizeNativeProcess(name, "")
		if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			continue
		}
		user, err := source.UserKey(ctx, record.PID)
		if errors.Is(err, procutil.ErrObservedProcessGone) {
			continue
		}
		if err != nil || strings.TrimSpace(user) == "" {
			return nil, nil, errors.New("cannot establish candidate owner")
		}
		if user != owner {
			continue
		}
		// Path access can be refused even for a readable name. Preserve an
		// unknown classification instead of guessing CLI versus desktop.
		executable, _ := source.Executable(ctx, record.PID)
		provider, kind := registry.RecognizeNativeProcess(name, executable)
		if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			continue
		}
		// Anchor native status to the process that passed ownership and provider checks.
		if identityErr == nil && !identity.IsZero() && identity.StartTime > 0 {
			verified, err := source.ObservationIdentity(ctx, record.PID)
			if err == nil && verified == identity {
				identities[record.PID] = identity
			}
		}
		result.TotalCount++
		if len(result.Processes) < limit {
			result.Processes = append(result.Processes, &leapmuxv1.NativeAgentProcess{
				Pid: int32(record.PID), ParentPid: int32(record.PPID),
				ExecutableName: name, AgentProvider: provider, Kind: kind,
			})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return result, identities, nil
}
