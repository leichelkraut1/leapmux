package agentreport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reportDirectory(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dir := filepath.Join(parent, "reports")
	require.NoError(t, prepareDirectory(dir))
	return dir
}

func fixtureReport(now time.Time) Report {
	return Report{SchemaVersion: 1, Provider: "codex", SessionID: "session-123",
		DisplayName: "Review dashboard", PID: 42, ProcessStartTime: now.Add(-time.Hour).UnixMilli(),
		State: "working", ReportedAt: now}
}

func putReport(t *testing.T, dir, name string, report Report) {
	t.Helper()
	data, err := json.Marshal(report)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
}

func TestWriteReadPreservesIdentityTimestampAndNormalizesName(t *testing.T) {
	t.Parallel()
	dir := reportDirectory(t)
	now := time.Now().UTC()
	report := fixtureReport(now)
	report.DisplayName = "  Review\n\u202edashboard\t " + strings.Repeat("x", 200)
	require.NoError(t, Write(dir, report))
	rows, err := Read(context.Background(), dir, now)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "working", rows[0].State)
	assert.Equal(t, report.PID, rows[0].PID)
	assert.Equal(t, report.ProcessStartTime, rows[0].ProcessStartTime)
	assert.True(t, report.ReportedAt.Equal(rows[0].ReportedAt))
	assert.Len(t, []rune(rows[0].DisplayName), maxDisplayNameRunes)
	assert.True(t, strings.HasPrefix(rows[0].DisplayName, "Review dashboard "))
	path := filepath.Join(dir, reportName(report.SessionID))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, checkPrivate(path, info))
}

func TestReadExpiresStateWithoutRefreshingTheReport(t *testing.T) {
	t.Parallel()
	dir := reportDirectory(t)
	now := time.Now().UTC()
	report := fixtureReport(now.Add(-FreshFor))
	putReport(t, dir, reportName(report.SessionID), report)
	rows, err := Read(context.Background(), dir, now)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "unknown", rows[0].State)
	assert.Equal(t, report.DisplayName, rows[0].DisplayName)
	assert.True(t, report.ReportedAt.Equal(rows[0].ReportedAt))
	rows, err = Read(context.Background(), dir, report.ReportedAt.Add(MaxAge+time.Nanosecond))
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestReadOmitsMalformedFutureAndMismatchedReports(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		change func(*Report)
	}{
		{"schema", func(r *Report) { r.SchemaVersion = 2 }},
		{"provider", func(r *Report) { r.Provider = "other" }},
		{"invalid ID", func(r *Report) { r.SessionID = "../private" }},
		{"different ID", func(r *Report) { r.SessionID = "different" }},
		{"PID", func(r *Report) { r.PID = 0 }},
		{"start time", func(r *Report) { r.ProcessStartTime = 0 }},
		{"future process", func(r *Report) { r.ProcessStartTime = now.Add(time.Second).UnixMilli() }},
		{"future report", func(r *Report) { r.ReportedAt = now.Add(time.Nanosecond) }},
		{"missing time", func(r *Report) { r.ReportedAt = time.Time{} }},
		{"expired", func(r *Report) { r.ReportedAt = now.Add(-MaxAge - time.Second) }},
		{"state", func(r *Report) { r.State = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := reportDirectory(t)
			report := fixtureReport(now)
			tc.change(&report)
			putReport(t, dir, reportName("session-123"), report)
			rows, err := Read(context.Background(), dir, now)
			require.NoError(t, err)
			assert.Empty(t, rows)
		})
	}
	for _, value := range []string{"{", "null", "{}", strings.Repeat(" ", maxReportBytes+1)} {
		dir := reportDirectory(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, reportName("bad")), []byte(value), 0o600))
		rows, err := Read(context.Background(), dir, now)
		require.NoError(t, err)
		assert.Empty(t, rows)
	}
	dir := reportDirectory(t)
	data, err := json.Marshal(fixtureReport(now))
	require.NoError(t, err)
	for _, suffix := range []string{", \"conversation\":\"private\"}", "} {"} {
		value := string(data[:len(data)-1]) + suffix
		require.NoError(t, os.WriteFile(filepath.Join(dir, reportName("session-123")), []byte(value), 0o600))
		rows, err := Read(context.Background(), dir, now)
		require.NoError(t, err)
		assert.Empty(t, rows)
	}
}

func TestWriteRejectsInvalidReportsBeforeCreatingFiles(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(reportDirectory(t), "new")
	report := fixtureReport(time.Now().Add(time.Hour))
	require.ErrorIs(t, Write(dir, report), ErrInvalidReport)
	_, err := os.Stat(dir)
	assert.True(t, os.IsNotExist(err))
	assert.False(t, ValidSessionID("../outside"))
	assert.False(t, ValidSessionID(strings.Repeat("x", 129)))
	assert.True(t, ValidSessionID("01a17aa7-1f1e-7542-80d6-f47a3101c521"))
}

func TestReadMissingCanceledAndDirectoryOverflow(t *testing.T) {
	t.Parallel()
	dir := reportDirectory(t)
	rows, err := Read(context.Background(), filepath.Join(dir, "missing"), time.Now())
	require.NoError(t, err)
	assert.Empty(t, rows)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Read(ctx, dir, time.Now())
	assert.ErrorIs(t, err, context.Canceled)
	for i := range maxDirectoryEntries + 1 {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("unrelated-%04d", i)), nil, 0o600))
	}
	_, err = Read(context.Background(), dir, time.Now())
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestReadReturnsNewestReportsAndAllowsMultipleSessionsPerProcess(t *testing.T) {
	t.Parallel()
	dir := reportDirectory(t)
	now := time.Now().UTC()
	for i := range maxReports + 3 {
		report := fixtureReport(now.Add(-time.Duration(i) * time.Second))
		report.SessionID = fmt.Sprintf("session-%04d", i)
		putReport(t, dir, reportName(report.SessionID), report)
	}
	rows, err := Read(context.Background(), dir, now)
	require.NoError(t, err)
	require.Len(t, rows, maxReports)
	assert.Equal(t, "session-0000", rows[0].SessionID)
	assert.Equal(t, fmt.Sprintf("session-%04d", maxReports-1), rows[len(rows)-1].SessionID)
	assert.Equal(t, rows[0].PID, rows[1].PID)
}

func TestConcurrentWritesPublishWholeReports(t *testing.T) {
	t.Parallel()
	dir := reportDirectory(t)
	now := time.Now().UTC()
	first := fixtureReport(now)
	first.DisplayName = strings.Repeat("a", maxDisplayNameRunes)
	second := first
	second.State = "waiting"
	second.DisplayName = strings.Repeat("b", maxDisplayNameRunes)
	require.NoError(t, Write(dir, first))
	start := make(chan struct{})
	errors := make(chan error, 12)
	var writers sync.WaitGroup
	for i := range 12 {
		writers.Go(func() {
			<-start
			report := first
			if i%2 != 0 {
				report = second
			}
			errors <- Write(dir, report)
		})
	}
	defer writers.Wait()
	close(start)
	for range 24 {
		data, err := os.ReadFile(filepath.Join(dir, reportName(first.SessionID)))
		require.NoError(t, err)
		var report Report
		require.NoError(t, json.Unmarshal(data, &report))
		if report.State == "working" {
			assert.Equal(t, first.DisplayName, report.DisplayName)
		} else {
			assert.Equal(t, "waiting", report.State)
			assert.Equal(t, second.DisplayName, report.DisplayName)
		}
	}
	writers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
