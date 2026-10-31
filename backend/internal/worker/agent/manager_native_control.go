package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Unknown/in-flight mutations are never evicted. Confirmed results may retire
// only after the original native lease is certainly expired; replaying that
// original payload then fails the provider's binding check before any write.
const maxNativeControlMutations = 2048

type nativeControlMutation struct {
	fingerprint [32]byte
	done        chan struct{}
	receipt     NativeSessionInputReceipt
	err         error
	retainUntil time.Time
}

type nativeControllerEntry struct {
	controller NativeSessionController
	mu         sync.Mutex
	mutations  map[string]*nativeControlMutation
	sessions   map[string]bool
}

func (m *Manager) nativeController(ctx context.Context, provider leapmuxv1.AgentProvider) (*nativeControllerEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	directory := m.nativeControlDirectory
	m.mu.RUnlock()
	m.nativeControlMu.Lock()
	defer m.nativeControlMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.nativeControlClosed {
		return nil, ErrNativeControlUnavailable
	}
	if entry := m.nativeControllers[provider]; entry != nil {
		return entry, nil
	}
	reg, ok := m.registry.Registration(provider)
	if !ok || reg.NewNativeSessionController == nil {
		return nil, ErrNativeControlUnavailable
	}
	opts := NativeSessionControllerOptions{}
	if directory != "" {
		opts.Directory = filepath.Join(directory, provider.String())
	}
	controller := reg.NewNativeSessionController(opts)
	if controller == nil {
		return nil, ErrNativeControlUnavailable
	}
	if m.nativeControllers == nil {
		m.nativeControllers = make(map[leapmuxv1.AgentProvider]*nativeControllerEntry)
	}
	entry := &nativeControllerEntry{controller: controller, mutations: make(map[string]*nativeControlMutation), sessions: make(map[string]bool)}
	m.nativeControllers[provider] = entry
	return entry, nil
}

func (m *Manager) InspectNativeSessionControl(ctx context.Context, provider leapmuxv1.AgentProvider, sessionID string) (NativeSessionControlState, error) {
	entry, err := m.nativeController(ctx, provider)
	if err != nil {
		return NativeSessionControlState{}, err
	}
	state, err := entry.controller.Inspect(ctx, sessionID)
	if state.OwnerPresent || state.BindingToken != "" || state.CanSend || state.CanInterrupt {
		entry.mu.Lock()
		entry.sessions[sessionID] = true
		entry.mu.Unlock()
	}
	return state, err
}

// mutate deduplicates simultaneous and repeated browser requests. A request ID
// cannot be reused with changed text, turn, owner, or session. In particular an
// uncertain transport result is remembered and never automatically resent.
func (e *nativeControllerEntry) mutate(ctx context.Context, requestID string, payload any, apply func() (NativeSessionInputReceipt, error)) (NativeSessionInputReceipt, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return NativeSessionInputReceipt{}, ErrNativeControlInvalid
	}
	fingerprint := sha256.Sum256(encoded)
	e.mu.Lock()
	if previous := e.mutations[requestID]; previous != nil {
		e.mu.Unlock()
		if previous.fingerprint != fingerprint {
			return NativeSessionInputReceipt{}, ErrNativeControlInvalid
		}
		select {
		case <-ctx.Done():
			return NativeSessionInputReceipt{}, ctx.Err()
		case <-previous.done:
			return previous.receipt, previous.err
		}
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return NativeSessionInputReceipt{}, err
	}
	e.pruneNativeMutations(time.Now())
	if len(e.mutations) >= maxNativeControlMutations {
		e.mu.Unlock()
		return NativeSessionInputReceipt{}, ErrNativeControlUnavailable
	}
	mutation := &nativeControlMutation{fingerprint: fingerprint, done: make(chan struct{}),
		retainUntil: time.Now().Add(2 * contracts.NativeSessionControlLeaseSeconds * time.Second)}
	e.mutations[requestID] = mutation
	e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		mutation.err = err
	} else {
		mutation.receipt, mutation.err = apply()
	}
	close(mutation.done)
	return mutation.receipt, mutation.err
}

// Caller holds e.mu. A closed done channel publishes the immutable result.
func (e *nativeControllerEntry) pruneNativeMutations(now time.Time) {
	for id, mutation := range e.mutations {
		if mutation.retainUntil.IsZero() || now.Before(mutation.retainUntil) {
			continue
		}
		select {
		case <-mutation.done:
			if mutation.err == nil || errors.Is(mutation.err, ErrNativeControlInvalid) ||
				errors.Is(mutation.err, ErrNativeControlStale) || errors.Is(mutation.err, ErrNativeControlUnavailable) {
				delete(e.mutations, id)
			}
		default:
		}
	}
}

func (m *Manager) SendNativeSessionInput(ctx context.Context, provider leapmuxv1.AgentProvider, input NativeSessionInput) (NativeSessionInputReceipt, error) {
	entry, err := m.nativeController(ctx, provider)
	if err != nil {
		return NativeSessionInputReceipt{}, err
	}
	return entry.mutate(ctx, input.RequestID, struct {
		Kind string
		NativeSessionInput
	}{"send", input}, func() (NativeSessionInputReceipt, error) { return entry.controller.Send(ctx, input) })
}

func (m *Manager) InterruptNativeSession(ctx context.Context, provider leapmuxv1.AgentProvider, input NativeSessionInterrupt) error {
	entry, err := m.nativeController(ctx, provider)
	if err != nil {
		return err
	}
	_, err = entry.mutate(ctx, input.RequestID, struct {
		Kind string
		NativeSessionInterrupt
	}{"interrupt", input}, func() (NativeSessionInputReceipt, error) {
		return NativeSessionInputReceipt{}, entry.controller.Interrupt(ctx, input)
	})
	return err
}

func (m *Manager) closeNativeControllers() {
	m.nativeControlMu.Lock()
	if m.nativeControlClosed {
		m.nativeControlMu.Unlock()
		return
	}
	m.nativeControlClosed = true
	entries := m.nativeControllers
	m.nativeControllers = nil
	m.nativeControlMu.Unlock()
	for _, entry := range entries {
		_ = entry.controller.Close()
	}
}

// ValidNativeControlLease lets provider implementations use the same clock rule
// without treating a stale history page or an observed process as permission.
func ValidNativeControlLease(state NativeSessionControlState, binding string, now time.Time) bool {
	return binding != "" && binding == state.BindingToken && !state.ObservedAt.IsZero() &&
		!state.ObservedAt.After(now.Add(5*time.Second)) && now.Before(state.ExpiresAt) &&
		now.Sub(state.ObservedAt) <= contracts.NativeSessionControlLeaseSeconds*time.Second &&
		state.ExpiresAt.Sub(state.ObservedAt) <= contracts.NativeSessionControlLeaseSeconds*time.Second
}
