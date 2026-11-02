package providerkit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeCLIHelperRun(t *testing.T, mode string, args ...string) NativeCLIRun {
	t.Helper()
	path, err := os.Executable()
	require.NoError(t, err)
	return NativeCLIRun{
		Program: path, Label: "Test CLI",
		Args:       append([]string{"-test.run=^TestRunNativeCLIHelperProcess$", "--", "native-cli-helper", mode}, args...),
		WorkingDir: t.TempDir(), Timeout: 30 * time.Second, MaxOutput: 4096,
	}
}

func TestRunNativeCLIHelperProcess(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" && i+2 < len(os.Args) && os.Args[i+1] == "native-cli-helper" {
			index = i + 2
			break
		}
	}
	if index < 0 {
		return
	}
	switch os.Args[index] {
	case "echo":
		dir, _ := os.Getwd()
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"args": os.Args[index+1:], "directory": dir,
			"hasControlToken":   os.Getenv("LEAPMUX_CONTROL_TOKEN") != "",
			"hasParentIdentity": os.Getenv("GROK_SESSION_ID") != "",
			"hasParentHelper":   os.Getenv(contracts.EnvAgentHelper) != "",
			"worker":            os.Getenv("LEAPMUX_WORKER"), "kept": os.Getenv("LEAPMUX_NATIVE_KEEP"),
		})
	case "large":
		fmt.Print(strings.Repeat("x", 4096))
	case "failure":
		fmt.Fprintln(os.Stderr, "test-cli-error")
		os.Exit(7)
	case "wait":
		conn, err := net.Dial("tcp", os.Args[index+1])
		if err != nil {
			os.Exit(8)
		}
		_, _ = fmt.Fprintln(conn, os.Getpid())
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	default:
		os.Exit(9)
	}
	os.Exit(0)
}

func TestRunNativeCLIPreservesArgumentsAndScrubsEnvironment(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_TOKEN", "test-control-secret")
	t.Setenv("GROK_SESSION_ID", "test-parent-session")
	t.Setenv(contracts.EnvAgentHelper, "test-parent-helper")
	t.Setenv("LEAPMUX_NATIVE_KEEP", "kept")
	run := nativeCLIHelperRun(t, "echo", "two words", "quoted\"argument")
	output, err := RunNativeCLI(context.Background(), run)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(output, &result))
	assert.Equal(t, []any{"two words", "quoted\"argument"}, result["args"])
	want, err := filepath.EvalSymlinks(run.WorkingDir)
	require.NoError(t, err)
	actual, err := filepath.EvalSymlinks(result["directory"].(string))
	require.NoError(t, err)
	assert.Equal(t, want, actual)
	assert.Equal(t, false, result["hasControlToken"])
	assert.Equal(t, false, result["hasParentIdentity"])
	assert.Equal(t, false, result["hasParentHelper"])
	assert.Equal(t, "1", result["worker"])
	assert.Equal(t, "kept", result["kept"])
}

func TestRunNativeCLIRejectsTruncatedOutput(t *testing.T) {
	t.Parallel()
	run := nativeCLIHelperRun(t, "large")
	run.MaxOutput = 32
	output, err := RunNativeCLI(context.Background(), run)
	require.ErrorContains(t, err, "exceeds 32 bytes")
	assert.Nil(t, output)
	assert.NotErrorIs(t, err, ErrNativeCLIUnavailable)
}

func TestRunNativeCLIFailureDoesNotPermitShellFallback(t *testing.T) {
	t.Parallel()
	run := nativeCLIHelperRun(t, "failure")
	output, err := RunNativeCLI(context.Background(), run)
	require.ErrorContains(t, err, "test-cli-error")
	assert.Nil(t, output)
	assert.NotErrorIs(t, err, ErrNativeCLIUnavailable)

	_, err = runNativeCLI(context.Background(), run, func(string) (string, error) {
		return filepath.Join(t.TempDir(), "absent.exe"), nil
	}, runtime.GOOS)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNativeCLIUnavailable, "a resolved program that cannot start must not be run again")
}

func TestRunNativeCLIUnavailableAndCurrentDirectoryGuard(t *testing.T) {
	t.Parallel()
	run := NativeCLIRun{Program: "claude", Timeout: 30 * time.Second, MaxOutput: 32}
	_, err := runNativeCLI(context.Background(), run, func(name string) (string, error) {
		assert.Equal(t, "claude", name)
		return "", exec.ErrNotFound
	}, "windows")
	require.ErrorIs(t, err, ErrNativeCLIUnavailable)
	_, err = runNativeCLI(context.Background(), run, func(string) (string, error) {
		return "claude.exe", &exec.Error{Name: "claude", Err: exec.ErrDot}
	}, "windows")
	require.ErrorIs(t, err, exec.ErrDot)
	assert.NotErrorIs(t, err, ErrNativeCLIUnavailable)
	_, err = runNativeCLI(context.Background(), run, func(string) (string, error) {
		return filepath.Join(t.TempDir(), "claude.CMD"), nil
	}, "windows")
	require.ErrorIs(t, err, ErrNativeCLIUnavailable)
}

func TestNativeCLIExecutableWindowsEligibility(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"claude.exe", true}, {"CLAUDE.EXE", true}, {"claude.com", true},
		{"claude.cmd", false}, {"claude.bat", false}, {"claude.ps1", false},
		{"claude.js", false}, {"claude.vbs", false}, {"claude.wsf", false},
		{"claude", false}, {"", false},
	} {
		assert.Equal(t, tc.want, nativeCLIExecutable(tc.path, "windows"), tc.path)
	}
	assert.True(t, nativeCLIExecutable("/usr/local/bin/claude", "darwin"))
}

func TestRunNativeCLIHoldsTheCallerDeadlineDuringLookup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := NativeCLIRun{Program: "claude", Timeout: 30 * time.Second}
	_, err := runNativeCLI(ctx, run, func(string) (string, error) {
		t.Fatal("canceled lookup must not execute")
		return "", nil
	}, runtime.GOOS)
	require.ErrorIs(t, err, context.Canceled)

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	_, err = runNativeCLI(ctx, run, func(string) (string, error) {
		cancel()
		return "claude.exe", nil
	}, runtime.GOOS)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrNativeCLIUnavailable)
}

func TestRunNativeCLICancellationReapsTheStartedProcess(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	require.NoError(t, listener.(*net.TCPListener).SetDeadline(time.Now().Add(30*time.Second)))
	run := nativeCLIHelperRun(t, "wait", listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		_, err := RunNativeCLI(ctx, run)
		done <- err
	}()
	conn, err := listener.Accept()
	require.NoError(t, err, "the helper must be running before cancellation")
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	require.NoError(t, err)
	identity, ok := procutil.IdentifyProcess(pid)
	require.True(t, ok)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrNativeCLIUnavailable)
	case <-time.After(30 * time.Second):
		t.Fatal("canceled native query did not finish cleanup")
	}
	assert.False(t, identity.Runs(), "the canceled native helper must be reaped")
}

func TestRunNativeCLIExpiredDeadlineSkipsLookup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := runNativeCLI(ctx, NativeCLIRun{Timeout: 30 * time.Second}, func(string) (string, error) {
		t.Fatal("expired lookup must not execute")
		return "", errors.New("unexpected lookup")
	}, runtime.GOOS)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
