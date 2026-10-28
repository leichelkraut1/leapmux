package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codex"
	"github.com/leapmux/leapmux/util/agentreport"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reportTestProcesses struct {
	records         []procutil.ProcessRecord
	names           map[int]string
	paths           map[int]string
	owners          map[int]string
	identities      map[int]procutil.ProcessIdentity
	descendants     map[int][]procutil.ProcessIdentity
	pathReads       []int
	identityReads   map[int]int
	replaceOnRead   map[int]int
	descendantReads int
}

func newReportTestProcesses() *reportTestProcesses {
	p := &reportTestProcesses{
		records: []procutil.ProcessRecord{{PID: 100, PPID: 90}, {PID: 90, PPID: 80}, {PID: 80, PPID: 70}, {PID: 70, PPID: 1}, {PID: 1}},
		names:   map[int]string{100: "leapmux", 90: "pwsh", 80: "codex", 70: "codex", 1: "system"},
		paths:   map[int]string{80: "/usr/local/bin/codex", 70: "/usr/local/bin/codex"},
		owners:  map[int]string{100: "owner", 80: "owner", 70: "owner"},
		identities: map[int]procutil.ProcessIdentity{
			100: {PID: 100, StartTime: 1000}, 80: {PID: 80, StartTime: 800}, 70: {PID: 70, StartTime: 700},
		},
		descendants: map[int][]procutil.ProcessIdentity{
			80: {{PID: 100, StartTime: 1000}}, 70: {{PID: 100, StartTime: 1000}},
		},
		identityReads: make(map[int]int), replaceOnRead: make(map[int]int),
	}
	return p
}

func (p *reportTestProcesses) Records() []procutil.ProcessRecord { return p.records }
func (p *reportTestProcesses) ShortName(_ context.Context, pid int) (string, error) {
	name, ok := p.names[pid]
	if !ok {
		return "", errors.New("private name error")
	}
	return name, nil
}
func (p *reportTestProcesses) UserKey(_ context.Context, pid int) (string, error) {
	owner, ok := p.owners[pid]
	if !ok {
		return "", errors.New("private owner error")
	}
	return owner, nil
}
func (p *reportTestProcesses) Executable(_ context.Context, pid int) (string, error) {
	p.pathReads = append(p.pathReads, pid)
	path, ok := p.paths[pid]
	if !ok {
		return "", errors.New("private executable error")
	}
	return path, nil
}
func (p *reportTestProcesses) ObservationIdentity(_ context.Context, pid int) (procutil.ProcessIdentity, error) {
	p.identityReads[pid]++
	identity, ok := p.identities[pid]
	if !ok {
		return procutil.ProcessIdentity{}, errors.New("private creation error")
	}
	if p.replaceOnRead[pid] == p.identityReads[pid] {
		identity.StartTime++
		p.identities[pid] = identity
	}
	return identity, nil
}
func (p *reportTestProcesses) VerifiedDescendants(_ context.Context, root procutil.ProcessIdentity) ([]procutil.ProcessIdentity, error) {
	p.descendantReads++
	return p.descendants[root.PID], nil
}

func reportTestDependencies(t *testing.T, table *reportTestProcesses) (reportDependencies, *[]agentreport.Report) {
	t.Helper()
	registry, err := agent.NewRegistry(codex.Registration())
	require.NoError(t, err)
	reports := new([]agentreport.Report)
	return reportDependencies{
		snapshot:  func(context.Context) (reportProcessSource, error) { return table, nil },
		recognize: registry.RecognizeNativeProcess,
		directory: func() (string, error) { return "/private/report-directory", nil },
		write: func(dir string, report agentreport.Report) error {
			assert.Equal(t, "/private/report-directory", dir)
			*reports = append(*reports, report)
			return nil
		},
		getenv: func(key string) string {
			assert.Equal(t, "CODEX_THREAD_ID", key, "no other environment values may be consulted")
			return "thread-from-environment"
		},
		now: func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) },
		pid: 100,
	}, reports
}

func TestReportSelectsNearestVerifiedCodexAncestor(t *testing.T) {
	t.Parallel()
	table := newReportTestProcesses()
	deps, reports := reportTestDependencies(t, table)
	var output bytes.Buffer
	err := runReportWith([]string{"--name", "Private chat name", "--status", "working"}, &output, deps)
	require.NoError(t, err)
	require.Len(t, *reports, 1)
	report := (*reports)[0]
	assert.Equal(t, 1, report.SchemaVersion)
	assert.Equal(t, "codex", report.Provider)
	assert.Equal(t, "thread-from-environment", report.SessionID)
	assert.Equal(t, "Private chat name", report.DisplayName)
	assert.Equal(t, 80, report.PID)
	assert.Equal(t, int64(800), report.ProcessStartTime)
	assert.Equal(t, "working", report.State)
	assert.Equal(t, deps.now(), report.ReportedAt)
	assert.Equal(t, 2, table.descendantReads, "ancestry is verified during selection and before writing")
	assert.NotContains(t, table.pathReads, 70, "a farther CLI must not win")

	var receipt map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &receipt))
	assert.Len(t, receipt, 4)
	assert.Equal(t, "working", receipt["status"])
	assert.Equal(t, report.SessionID, receipt["sessionId"])
	assert.Equal(t, "2026-10-28T12:00:00Z", receipt["reportedAt"])
	assert.Equal(t, report.ReportedAt.Add(agentreport.FreshFor).Format(time.RFC3339), receipt["expiresAt"])
	assert.NotContains(t, output.String(), "Private chat")
	assert.NotContains(t, output.String(), "/private")
	assert.NotContains(t, output.String(), "pid")
}

func TestReportExplicitSessionAndProcess(t *testing.T) {
	t.Parallel()
	table := newReportTestProcesses()
	deps, reports := reportTestDependencies(t, table)
	deps.getenv = func(string) string { t.Fatal("an explicit session must not read environment"); return "" }
	require.NoError(t, runReportWith([]string{"--session-id", "chosen-session", "--pid", "70", "--status", "waiting"}, io.Discard, deps))
	require.Len(t, *reports, 1)
	assert.Equal(t, "chosen-session", (*reports)[0].SessionID)
	assert.Equal(t, 70, (*reports)[0].PID)
	assert.Equal(t, "waiting", (*reports)[0].State)
	assert.Zero(t, table.descendantReads, "explicit selection requires identity, not ancestry")
}

func TestReportRejectsInvalidInputBeforeProcessAccess(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--status", "working", "--session-id", "../secret"},
		{"--status", "working", "--session-id", ""},
		{"--status", "working", "--session-id", "line\nbreak"},
		{"--status", "busy"},
		{"--session-id", "session"},
		{"--status", "idle", "--pid", "0"},
		{"--status", "idle", "--pid", "-1"},
		{"--status", "idle", "--pid", "9999999999"},
		{"--status", "idle", "--pid", "invalid"},
		{"--status", "idle", "unexpected"},
		{"--unknown", "secret"},
	} {
		deps, reports := reportTestDependencies(t, newReportTestProcesses())
		deps.snapshot = func(context.Context) (reportProcessSource, error) {
			t.Fatal("invalid input must not inspect processes")
			return nil, nil
		}
		var output bytes.Buffer
		err := runReportWith(args, &output, deps)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "secret")
		assert.Empty(t, output.String())
		assert.Empty(t, *reports)
	}
	for _, env := range []string{"", "bad/path", "bad\nvalue"} {
		deps, reports := reportTestDependencies(t, newReportTestProcesses())
		deps.getenv = func(string) string { return env }
		deps.snapshot = func(context.Context) (reportProcessSource, error) {
			t.Fatal("invalid inherited ID must not inspect processes")
			return nil, nil
		}
		require.Error(t, runReportWith([]string{"--status", "idle"}, io.Discard, deps))
		assert.Empty(t, *reports)
	}
}

func TestReportRejectsDesktopUnknownAndOtherUserProcesses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*reportTestProcesses)
	}{
		{"desktop", func(p *reportTestProcesses) { p.paths[80] = "/Applications/Codex.app/Contents/MacOS/Codex" }},
		{"unknown executable", func(p *reportTestProcesses) { p.paths[80] = "/unclassified/codex" }},
		{"node wrapper", func(p *reportTestProcesses) { p.names[80] = "node" }},
		{"other user", func(p *reportTestProcesses) { p.owners[80] = "other" }},
		{"owner unavailable", func(p *reportTestProcesses) { delete(p.owners, 80) }},
		{"path unavailable", func(p *reportTestProcesses) { delete(p.paths, 80) }},
		{"exited", func(p *reportTestProcesses) { delete(p.identities, 80) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := newReportTestProcesses()
			tc.change(table)
			deps, reports := reportTestDependencies(t, table)
			var output bytes.Buffer
			err := runReportWith([]string{"--pid", "80", "--status", "idle"}, &output, deps)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private")
			assert.Empty(t, *reports)
			assert.Empty(t, output.String())
			if tc.name == "other user" || tc.name == "owner unavailable" {
				assert.Empty(t, table.pathReads, "paths must not be read before same-user ownership")
			}
		})
	}
}

func TestReportDoesNotChooseAnUnrelatedCodexProcess(t *testing.T) {
	t.Parallel()
	table := newReportTestProcesses()
	table.records = []procutil.ProcessRecord{{PID: 100, PPID: 90}, {PID: 90, PPID: 1}, {PID: 1}, {PID: 80, PPID: 1}}
	deps, reports := reportTestDependencies(t, table)
	err := runReportWith([]string{"--status", "idle"}, io.Discard, deps)
	require.ErrorContains(t, err, "no same-user native Codex CLI ancestor")
	assert.Empty(t, table.pathReads)
	assert.Empty(t, *reports)
}

func TestReportRejectsAncestryCyclesAndReparenting(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cycle", "reparented", "reused caller"} {
		t.Run(mode, func(t *testing.T) {
			table := newReportTestProcesses()
			switch mode {
			case "cycle":
				table.records = []procutil.ProcessRecord{{PID: 100, PPID: 90}, {PID: 90, PPID: 100}}
			case "reparented":
				table.descendants[80] = nil
			case "reused caller":
				table.descendants[80] = []procutil.ProcessIdentity{{PID: 100, StartTime: 2000}}
			}
			deps, reports := reportTestDependencies(t, table)
			require.Error(t, runReportWith([]string{"--status", "idle"}, io.Discard, deps))
			assert.Empty(t, *reports)
		})
	}
}

func TestReportRejectsPIDReuseDuringAndAfterSelection(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"during ownership check", "before write", "ownership changed", "ancestry changed"} {
		t.Run(mode, func(t *testing.T) {
			table := newReportTestProcesses()
			deps, reports := reportTestDependencies(t, table)
			if mode == "during ownership check" {
				table.replaceOnRead[80] = 2
			} else {
				deps.directory = func() (string, error) {
					switch mode {
					case "before write":
						table.identities[80] = procutil.ProcessIdentity{PID: 80, StartTime: 1600}
					case "ownership changed":
						table.owners[80] = "other"
					case "ancestry changed":
						table.descendants[80] = nil
					}
					return "/private/report-directory", nil
				}
			}
			require.Error(t, runReportWith([]string{"--status", "working"}, io.Discard, deps))
			assert.Empty(t, *reports)
		})
	}
}

func TestReportSanitizesStorageAndSnapshotErrors(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"snapshot", "directory", "write"} {
		t.Run(mode, func(t *testing.T) {
			deps, _ := reportTestDependencies(t, newReportTestProcesses())
			privateErr := errors.New("/private/credentials must not be printed")
			switch mode {
			case "snapshot":
				deps.snapshot = func(context.Context) (reportProcessSource, error) { return nil, privateErr }
			case "directory":
				deps.directory = func() (string, error) { return "", privateErr }
			case "write":
				deps.write = func(string, agentreport.Report) error { return privateErr }
			}
			var output bytes.Buffer
			err := runReportWith([]string{"--status", "idle"}, &output, deps)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "/private")
			assert.Empty(t, output.String())
		})
	}
}

func TestReportHelpDoesNotInspectEnvironmentOrProcesses(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	require.NoError(t, runReportWith([]string{"--help"}, &output, reportDependencies{}))
	assert.Contains(t, output.String(), "Usage: leapmux report")
	assert.Contains(t, output.String(), "CODEX_THREAD_ID")
}

func TestReportTopLevelRegistration(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"report", "rep"} {
		var stdout, stderr bytes.Buffer
		called := false
		code := runCLI([]string{command, "--status", "idle"}, &stdout, &stderr, cliRunners{
			runReport: func(args []string, output io.Writer) error {
				called = true
				assert.Equal(t, []string{"--status", "idle"}, args)
				_, err := io.WriteString(output, "receipt")
				return err
			},
		})
		assert.Equal(t, 0, code)
		assert.True(t, called)
		assert.Equal(t, "receipt", stdout.String())
		assert.Empty(t, stderr.String())
	}
	assert.Contains(t, usageText, reportSummary)
}

func TestReportCandidateHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	table := newReportTestProcesses()
	deps, _ := reportTestDependencies(t, table)
	_, _, err := reportCandidate(ctx, table, 80, "owner", deps.recognize)
	require.Error(t, err)
	assert.Empty(t, table.pathReads)
}

var _ reportProcessSource = (*reportTestProcesses)(nil)
