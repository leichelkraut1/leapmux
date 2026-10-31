// Package agentreport stores explicit agent status reports without conversation data.
// Reports are agent claims, not observations of provider runtime state.
package agentreport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

const (
	// FreshFor bounds how long a report may describe current activity.
	FreshFor = 15 * time.Minute
	// MaxAge bounds how long a stale report remains visible as unknown.
	MaxAge              = 24 * time.Hour
	maxReportBytes      = 8 << 10
	maxDirectoryEntries = 4096
	maxReports          = 256
	maxDisplayNameRunes = 160
)

var (
	validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	// ErrInvalidReport contains no report contents or filesystem paths.
	ErrInvalidReport = errors.New("invalid agent status report")
	// ErrUnavailable means the report directory could not be safely inspected.
	ErrUnavailable = errors.New("agent status reports are unavailable")
)

// Report contains only explicitly supplied status and process identity metadata.
type Report struct {
	SchemaVersion    int       `json:"schemaVersion"`
	Provider         string    `json:"provider"`
	SessionID        string    `json:"sessionId"`
	DisplayName      string    `json:"displayName"`
	PID              int       `json:"pid"`
	ProcessStartTime int64     `json:"processStartTime"`
	State            string    `json:"state"`
	ReportedAt       time.Time `json:"reportedAt"`
}

// Directory returns the shared Codex report directory without creating it.
func Directory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", ErrUnavailable
	}
	// Resolve a platform home alias once; report-tree symlinks are refused below.
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", ErrUnavailable
	}
	return filepath.Join(home, ".config", "leapmux", "agent-reports", "codex"), nil
}

// ValidSessionID reports whether an opaque ID is safe for the report filename.
func ValidSessionID(id string) bool { return validID.MatchString(id) }
func reportName(id string) string   { return "session-" + id + ".json" }

// Write atomically replaces one report. It never substitutes a new timestamp.
func Write(dir string, report Report) error {
	if !validReport(report, time.Now()) {
		return ErrInvalidReport
	}
	report.DisplayName = normalizeName(report.DisplayName)
	report.ReportedAt = report.ReportedAt.UTC()
	data, err := json.Marshal(report)
	if err != nil || len(data) > maxReportBytes {
		return ErrInvalidReport
	}
	if err := prepareDirectory(dir); err != nil {
		return ErrUnavailable
	}
	destination := filepath.Join(dir, reportName(report.SessionID))
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() || checkPrivate(destination, info) != nil {
			return ErrUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	if err := atomicfile.WriteFile(destination, data, 0o600); err != nil {
		return ErrUnavailable
	}
	return nil
}

// Read returns the newest bounded set of valid reports. Stale records keep their
// original timestamp and name, but have state unknown. Invalid records are omitted.
// A missing directory is an empty opt-in; unsafe or unbounded reads fail closed.
func Read(ctx context.Context, dir string, now time.Time) ([]Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkDirectoryPath(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || checkPrivate(dir, info) != nil {
		return nil, ErrUnavailable
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, ErrUnavailable
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(maxDirectoryEntries + 1)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > maxDirectoryEntries {
		return nil, ErrUnavailable
	}
	reports := make([]Report, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "session-"), ".json")
		if !ValidSessionID(id) {
			continue
		}
		report, ok := readReport(root, dir, name, now)
		if !ok || report.SessionID != id {
			continue
		}
		if !now.Before(report.ReportedAt.Add(FreshFor)) {
			report.State = "unknown"
		}
		reports = append(reports, report)
	}
	slices.SortFunc(reports, func(a, b Report) int {
		if compared := b.ReportedAt.Compare(a.ReportedAt); compared != 0 {
			return compared
		}
		return strings.Compare(a.SessionID, b.SessionID)
	})
	if len(reports) > maxReports {
		reports = reports[:maxReports]
	}
	return reports, nil
}

func readReport(root *os.Root, dir, name string, now time.Time) (Report, bool) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxReportBytes || checkPrivate(filepath.Join(dir, name), info) != nil {
		return Report{}, false
	}
	file, err := root.Open(name)
	if err != nil {
		return Report{}, false
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return Report{}, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReportBytes+1))
	if err != nil || len(data) > maxReportBytes || !utf8.Valid(data) {
		return Report{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report Report
	if decoder.Decode(&report) != nil || !validReport(report, now) {
		return Report{}, false
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return Report{}, false
	}
	report.DisplayName = normalizeName(report.DisplayName)
	return report, true
}

func validReport(report Report, now time.Time) bool {
	if report.SchemaVersion != 1 || report.Provider != "codex" || !ValidSessionID(report.SessionID) ||
		report.PID <= 0 || report.PID > math.MaxInt32 || report.ProcessStartTime <= 0 ||
		report.ReportedAt.IsZero() || report.ReportedAt.After(now) || now.Sub(report.ReportedAt) > MaxAge ||
		report.ProcessStartTime > report.ReportedAt.UnixMilli() || !utf8.ValidString(report.DisplayName) {
		return false
	}
	return report.State == "working" || report.State == "waiting" || report.State == "idle"
}

func normalizeName(name string) string {
	name = strings.Join(strings.FieldsFunc(name, func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsControl(char) || unicode.In(char, unicode.Cf)
	}), " ")
	runes := []rune(name)
	if len(runes) > maxDisplayNameRunes {
		runes = runes[:maxDisplayNameRunes]
	}
	return string(runes)
}

func prepareDirectory(dir string) error {
	if err := checkDirectoryPath(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := mkdirPrivate(dir); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := checkDirectoryPath(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return ErrUnavailable
	}
	return restrictDirectory(dir, info)
}

// Refuse symlink components instead of following a report directory elsewhere.
func checkDirectoryPath(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return ErrUnavailable
	}
	var missing error
	for current := dir; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			missing = err
		} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return missing
}
