package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"os"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bootstrap"
	"github.com/leapmux/leapmux/util/agentreport"
	"github.com/leapmux/leapmux/util/procutil"
)

const reportSummary = "Report this Codex session's activity to the local Worker"
const reportProcessTimeout = 5 * time.Second
const maxReportAncestorDepth = 128

const reportUsage = `Report this Codex session's own activity. This does not observe or control a chat.

Usage: leapmux report --status working|waiting|idle [flags]

Options:
  --session-id ID   Session identifier (defaults to CODEX_THREAD_ID)
  --name TITLE      Optional chat name
  --status STATE   working, waiting, or idle
  --pid PID        Explicit same-user native Codex CLI process; otherwise use
                   the nearest verified Codex CLI ancestor
  -h, --help       Print help

Reports expire unless refreshed. No hooks or provider settings are changed.
`

type reportProcessSource interface {
	Records() []procutil.ProcessRecord
	ShortName(context.Context, int) (string, error)
	UserKey(context.Context, int) (string, error)
	Executable(context.Context, int) (string, error)
	ObservationIdentity(context.Context, int) (procutil.ProcessIdentity, error)
	VerifiedDescendants(context.Context, procutil.ProcessIdentity) ([]procutil.ProcessIdentity, error)
}

type reportDependencies struct {
	snapshot  func(context.Context) (reportProcessSource, error)
	recognize func(string, string) (leapmuxv1.AgentProvider, leapmuxv1.NativeAgentProcessKind)
	directory func() (string, error)
	write     func(string, agentreport.Report) error
	getenv    func(string) string
	now       func() time.Time
	pid       int
}

type reportTarget struct {
	identity procutil.ProcessIdentity
	caller   procutil.ProcessIdentity
	owner    string
	ancestor bool
}

func runReport(args []string, stdout io.Writer) error {
	return runReportWith(args, stdout, reportDependencies{
		snapshot: func(ctx context.Context) (reportProcessSource, error) {
			return procutil.SnapshotProcessTable(ctx)
		},
		recognize: bootstrap.NativeProcessRecognizer(),
		directory: agentreport.Directory,
		write:     agentreport.Write,
		getenv:    os.Getenv,
		now:       time.Now,
		pid:       os.Getpid(),
	})
}

func runReportWith(args []string, stdout io.Writer, deps reportDependencies) error {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	sessionID := flags.String("session-id", "", "")
	name := flags.String("name", "", "")
	state := flags.String("status", "", "")
	pid := flags.Int("pid", 0, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(stdout, reportUsage)
			return err
		}
		return errors.New("invalid report flags; use leapmux report --help")
	}
	if flags.NArg() != 0 {
		return errors.New("report does not accept positional arguments")
	}
	sessionSet, pidSet := false, false
	flags.Visit(func(f *flag.Flag) {
		sessionSet = sessionSet || f.Name == "session-id"
		pidSet = pidSet || f.Name == "pid"
	})
	if !sessionSet {
		*sessionID = deps.getenv("CODEX_THREAD_ID")
	}
	if !agentreport.ValidSessionID(*sessionID) {
		return errors.New("a valid session ID is required via --session-id or CODEX_THREAD_ID")
	}
	switch *state {
	case "working", "waiting", "idle":
	default:
		return errors.New("--status must be working, waiting, or idle")
	}
	if pidSet && (*pid <= 0 || *pid > math.MaxInt32) {
		return errors.New("--pid must identify a running native Codex CLI process")
	}

	ctx, cancel := context.WithTimeout(context.Background(), reportProcessTimeout)
	defer cancel()
	table, err := deps.snapshot(ctx)
	if err != nil || table == nil {
		return errors.New("process metadata is unavailable; no report was written")
	}
	target, err := selectReportTarget(ctx, table, deps.pid, *pid, pidSet, deps.recognize)
	if err != nil {
		return err
	}
	dir, err := deps.directory()
	if err != nil {
		return errors.New("private report storage is unavailable; no report was written")
	}
	// Re-read ownership, executable classification, and creation time immediately
	// before writing. A reused PID must never inherit another process's report.
	if err := verifyReportTarget(ctx, table, target, deps.recognize); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errors.New("process verification expired; no report was written")
	}
	now := deps.now().UTC()
	report := agentreport.Report{
		SchemaVersion: 1, Provider: "codex", SessionID: *sessionID, DisplayName: *name,
		PID: target.identity.PID, ProcessStartTime: target.identity.StartTime,
		State: *state, ReportedAt: now,
	}
	if err := deps.write(dir, report); err != nil {
		// Filesystem errors can include private paths. The caller needs the
		// category, not those details or any content supplied as a chat name.
		return errors.New("could not save the report; check local report storage permissions")
	}
	return json.NewEncoder(stdout).Encode(struct {
		Status     string    `json:"status"`
		SessionID  string    `json:"sessionId"`
		ReportedAt time.Time `json:"reportedAt"`
		ExpiresAt  time.Time `json:"expiresAt"`
	}{report.State, report.SessionID, now, now.Add(agentreport.FreshFor)})
}

func selectReportTarget(ctx context.Context, source reportProcessSource, callerPID, explicitPID int, explicit bool,
	recognize func(string, string) (leapmuxv1.AgentProvider, leapmuxv1.NativeAgentProcessKind),
) (reportTarget, error) {
	owner, err := source.UserKey(ctx, callerPID)
	if err != nil || strings.TrimSpace(owner) == "" {
		return reportTarget{}, errors.New("cannot establish the reporting user's identity")
	}
	caller, err := source.ObservationIdentity(ctx, callerPID)
	if err != nil || caller.IsZero() || caller.StartTime <= 0 {
		return reportTarget{}, errors.New("cannot establish the reporting process identity")
	}
	if explicit {
		identity, matches, err := reportCandidate(ctx, source, explicitPID, owner, recognize)
		if err != nil {
			return reportTarget{}, err
		}
		if !matches {
			return reportTarget{}, errors.New("--pid is not a recognized native Codex CLI process owned by this user")
		}
		return reportTarget{identity: identity, caller: caller, owner: owner}, nil
	}

	parents := make(map[int]int)
	for _, process := range source.Records() {
		parents[process.PID] = process.PPID
	}
	seen := map[int]bool{callerPID: true}
	for parent, depth := parents[callerPID], 0; parent > 0 && depth < maxReportAncestorDepth; parent, depth = parents[parent], depth+1 {
		if seen[parent] {
			return reportTarget{}, errors.New("process ancestry is inconsistent; no report was written")
		}
		seen[parent] = true
		identity, matches, err := reportCandidate(ctx, source, parent, owner, recognize)
		if err != nil {
			return reportTarget{}, err
		}
		if matches {
			target := reportTarget{identity: identity, caller: caller, owner: owner, ancestor: true}
			if err := verifyReportAncestry(ctx, source, target); err != nil {
				return reportTarget{}, err
			}
			return target, nil
		}
	}
	return reportTarget{}, errors.New("no same-user native Codex CLI ancestor found; use --pid only for this session's verified Codex CLI process")
}

// reportCandidate reads only process metadata and delegates executable recognition
// to the provider. A basename alone cannot distinguish a CLI from desktop helpers.
func reportCandidate(ctx context.Context, source reportProcessSource, pid int, owner string,
	recognize func(string, string) (leapmuxv1.AgentProvider, leapmuxv1.NativeAgentProcessKind),
) (procutil.ProcessIdentity, bool, error) {
	if err := ctx.Err(); err != nil {
		return procutil.ProcessIdentity{}, false, errors.New("process verification expired; no report was written")
	}
	name, err := source.ShortName(ctx, pid)
	if err != nil {
		return procutil.ProcessIdentity{}, false, errors.New("cannot read process metadata; no report was written")
	}
	provider, _ := recognize(name, "")
	if provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX {
		return procutil.ProcessIdentity{}, false, nil
	}
	before, err := source.ObservationIdentity(ctx, pid)
	if err != nil || before.IsZero() || before.StartTime <= 0 {
		return procutil.ProcessIdentity{}, false, errors.New("cannot establish Codex process identity; no report was written")
	}
	user, err := source.UserKey(ctx, pid)
	if err != nil || strings.TrimSpace(user) == "" {
		return procutil.ProcessIdentity{}, false, errors.New("cannot establish Codex process owner; no report was written")
	}
	if user != owner {
		return procutil.ProcessIdentity{}, false, nil
	}
	path, err := source.Executable(ctx, pid)
	if err != nil {
		return procutil.ProcessIdentity{}, false, errors.New("cannot verify the Codex executable; no report was written")
	}
	provider, kind := recognize(name, path)
	if provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX ||
		kind != leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI {
		return procutil.ProcessIdentity{}, false, nil
	}
	after, err := source.ObservationIdentity(ctx, pid)
	if err != nil || before != after {
		return procutil.ProcessIdentity{}, false, errors.New("the Codex process identity changed; no report was written")
	}
	return before, true, nil
}

func verifyReportAncestry(ctx context.Context, source reportProcessSource, target reportTarget) error {
	descendants, err := source.VerifiedDescendants(ctx, target.identity)
	if err != nil {
		return errors.New("cannot verify the Codex ancestor; no report was written")
	}
	for _, descendant := range descendants {
		if descendant == target.caller {
			return nil
		}
	}
	return errors.New("the Codex process is no longer a verified ancestor; no report was written")
}

func verifyReportTarget(ctx context.Context, source reportProcessSource, target reportTarget,
	recognize func(string, string) (leapmuxv1.AgentProvider, leapmuxv1.NativeAgentProcessKind),
) error {
	current, matches, err := reportCandidate(ctx, source, target.identity.PID, target.owner, recognize)
	if err != nil {
		return err
	}
	if !matches || current != target.identity {
		return errors.New("the Codex process identity changed; no report was written")
	}
	if target.ancestor {
		return verifyReportAncestry(ctx, source, target)
	}
	return nil
}
