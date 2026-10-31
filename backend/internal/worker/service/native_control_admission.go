package service

import (
	"context"
	"errors"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var errNativeSessionExternallyControlled = errors.New("this native chat still has an enrolled external owner or unconfirmed input; finish it in its original app before resuming in LeapMux")

type nativeControlAdmissionKey struct {
	provider leapmuxv1.AgentProvider
	session  string
}

type nativeControlAdmissionEntry struct {
	permit chan struct{}
	refs   int
}

// Admission is keyed by native identity, rather than a LeapMux agent ID. An
// external mutation and a new managed resume have different agent identities.
// Refcounts bound entries to callers holding or waiting for the permit.
type nativeControlAdmission struct {
	mu      sync.Mutex
	entries map[nativeControlAdmissionKey]*nativeControlAdmissionEntry
}

func (a *nativeControlAdmission) acquire(ctx context.Context, provider leapmuxv1.AgentProvider, session string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := nativeControlAdmissionKey{provider, session}
	a.mu.Lock()
	if a.entries == nil {
		a.entries = make(map[nativeControlAdmissionKey]*nativeControlAdmissionEntry)
	}
	entry := a.entries[key]
	if entry == nil {
		entry = &nativeControlAdmissionEntry{permit: make(chan struct{}, 1)}
		entry.permit <- struct{}{}
		a.entries[key] = entry
	}
	entry.refs++
	a.mu.Unlock()
	drop := func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		entry.refs--
		if entry.refs == 0 {
			delete(a.entries, key)
		}
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-entry.permit:
		if err := ctx.Err(); err != nil {
			entry.permit <- struct{}{}
			drop()
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(func() { entry.permit <- struct{}{}; drop() }) }, nil
	}
}

func (svc *Service) admitNativeResume(ctx context.Context, provider leapmuxv1.AgentProvider, session string) (func(), error) {
	release, err := svc.nativeControlAdmissions.acquire(ctx, provider, session)
	if err != nil {
		return nil, err
	}
	if svc.Agents == nil {
		release()
		return nil, errNativeSessionExternallyControlled
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	state, exists, err := svc.Agents.InspectExistingNativeSessionControl(probeCtx, provider, session)
	cancel()
	if !exists && (err == nil || errors.Is(err, agent.ErrNativeControlUnavailable)) {
		return release, nil
	}
	// A failed or inconsistent probe of an existing bridge is not evidence that
	// its owner/queued input has gone. An ordinary unsupported resume stays valid.
	if err != nil || state.SessionID != session || nativeResumeConflicts(state) {
		release()
		return nil, errNativeSessionExternallyControlled
	}
	return release, nil
}

func nativeResumeConflicts(state agent.NativeSessionControlState) bool {
	if state.OwnerPresent {
		return true
	}
	if state.BindingToken != "" {
		// Inspect is expected to issue a fresh lease. A malformed/expired lease
		// from an existing owner also cannot establish safe absence.
		return true
	}
	if state.CanSend || state.CanInterrupt {
		return true
	}
	for _, input := range state.RecentInputs {
		switch input.State {
		case leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNSPECIFIED,
			leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED,
			leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED,
			leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN:
			return true
		case leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED,
			leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DROPPED,
			leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED:
			// Confirmed outcomes alone do not imply that an owner is still live.
		}
	}
	return false
}
