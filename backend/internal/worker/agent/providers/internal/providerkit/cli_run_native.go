package providerkit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
)

// ErrNativeCLIUnavailable means lookup found no directly executable program.
// Only this error permits a caller to try its existing shell-based launcher.
var ErrNativeCLIUnavailable = errors.New("native CLI executable is unavailable")

// NativeCLIRun describes a short CLI query that does not need shell profiles.
type NativeCLIRun struct {
	Program    string
	Label      string
	Args       []string
	WorkingDir string
	Timeout    time.Duration
	MaxOutput  int
}

// RunNativeCLI resolves the program from the Worker's PATH and runs it directly.
// Shell scripts on Windows require the caller's shell fallback. A program that
// was found but failed to start or finish is never reported as unavailable.
func RunNativeCLI(ctx context.Context, run NativeCLIRun) ([]byte, error) {
	return runNativeCLI(ctx, run, exec.LookPath, runtime.GOOS)
}

func runNativeCLI(ctx context.Context, run NativeCLIRun, lookPath func(string) (string, error), goos string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, run.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := lookPath(run.Program)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if errors.Is(err, exec.ErrDot) {
		return nil, err
	}
	if err != nil || !nativeCLIExecutable(path, goos) {
		return nil, ErrNativeCLIUnavailable
	}
	// Freeze relative PATH entries before changing the child's working directory.
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, ErrNativeCLIUnavailable
	}
	cmd := exec.CommandContext(ctx, path, run.Args...)
	if info, err := os.Stat(run.WorkingDir); err == nil && info.IsDir() {
		cmd.Dir = run.WorkingDir
	}
	envutil.ScrubAppImageEnv(cmd)
	cmd.Env = FinalizeAgentEnv(cmd.Environ(), agent.Options{})
	procutil.HideConsoleWindow(cmd)
	procutil.DetachFromTerminal(cmd)
	return runCLICommand(ctx, cmd, CLIRun{
		Label: run.Label, Args: run.Args, Timeout: run.Timeout, MaxOutput: run.MaxOutput,
	}, "")
}

func nativeCLIExecutable(path, goos string) bool {
	if path == "" {
		return false
	}
	if goos != "windows" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".exe" || ext == ".com"
}
