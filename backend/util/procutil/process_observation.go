package procutil

import (
	"context"
	"errors"
)

// ErrObservedProcessGone means OS metadata confirms the observed process exited.
// Callers may skip a candidate with this error, but must still establish their own owner.
var ErrObservedProcessGone = errors.New("observed process exited")

// UserKey reads an opaque OS ownership key only; it never reads arguments or environment.
func (t *ProcessTable) UserKey(ctx context.Context, pid int) (string, error) {
	if t == nil || t.byPID[pid] == nil {
		return "", errors.New("process absent from snapshot")
	}
	key, err := processUserKey(ctx, t.byPID[pid])
	if err != nil && (processGone(pid, err) || platformObservationGone(ctx, pid, err)) {
		return "", ErrObservedProcessGone
	}
	return key, err
}

// Executable reads the executable path for local classification only.
// Callers must not publish it or use it to infer native session activity.
func (t *ProcessTable) Executable(ctx context.Context, pid int) (string, error) {
	if t == nil || t.byPID[pid] == nil {
		return "", errors.New("process absent from snapshot")
	}
	return t.byPID[pid].ExeWithContext(ctx)
}

// ShortName reads only OS process-name metadata. It never expands a truncated
// name from command arguments or environment. Unreadable metadata is an error;
// a process that disappeared during the snapshot is simply absent.
func (t *ProcessTable) ShortName(ctx context.Context, pid int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t == nil || t.byPID[pid] == nil {
		return "", errors.New("process absent from snapshot")
	}
	name, err := shortProcessName(ctx, t.byPID[pid])
	if err != nil && (processGone(pid, err) || platformObservationGone(ctx, pid, err)) {
		return "", nil
	}
	return name, err
}
