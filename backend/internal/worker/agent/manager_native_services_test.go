package agent_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testNativeServiceObserver struct {
	inspections atomic.Int32
	closes      atomic.Int32
}

func (o *testNativeServiceObserver) Inspect(context.Context) []agent.NativeServiceObservation {
	o.inspections.Add(1)
	return []agent.NativeServiceObservation{{SourceID: "test-source"}}
}
func (o *testNativeServiceObserver) Close() error { o.closes.Add(1); return nil }

func TestManagerOwnsOneLazyNativeObserverAndClosesItOnce(t *testing.T) {
	var factories atomic.Int32
	var mu sync.Mutex
	var created []*testNativeServiceObserver
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	reg.NewNativeServiceObserver = func() agent.NativeServiceObserver {
		factories.Add(1)
		observer := &testNativeServiceObserver{}
		mu.Lock()
		created = append(created, observer)
		mu.Unlock()
		return observer
	}
	registry, err := agent.NewRegistry(reg)
	require.NoError(t, err)
	manager := agent.NewManager(registry, nil)
	assert.Zero(t, factories.Load())
	var requests sync.WaitGroup
	for range 12 {
		requests.Add(1)
		go func() { defer requests.Done(); manager.InspectNativeServices(context.Background()) }()
	}
	requests.Wait()
	require.Equal(t, int32(1), factories.Load())
	require.Len(t, created, 1)
	assert.Equal(t, int32(12), created[0].inspections.Load())
	manager.StopAll()
	manager.StopAll()
	assert.Equal(t, int32(1), created[0].closes.Load())
	assert.Empty(t, manager.InspectNativeServices(context.Background()))
	assert.Equal(t, int32(1), factories.Load())
	other := agent.NewManager(registry, nil)
	defer other.StopAll()
	result := other.InspectNativeServices(context.Background())
	require.Equal(t, int32(2), factories.Load())
	require.Len(t, result[leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX], 1)
	assert.Zero(t, created[1].closes.Load())
}
func TestManagerDoesNotCreateNativeObserverForCanceledInspection(t *testing.T) {
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	reg.NewNativeServiceObserver = func() agent.NativeServiceObserver { t.Fatal("unexpected factory"); return nil }
	registry, err := agent.NewRegistry(reg)
	require.NoError(t, err)
	manager := agent.NewManager(registry, nil)
	defer manager.StopAll()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Empty(t, manager.InspectNativeServices(ctx))
}
