package agent

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// InspectNativeServices lazily constructs one observer per registered provider.
// Factories only construct; observers start passive work on demand and expire it
// themselves. Independent Managers never share a connection or subscription.
func (m *Manager) InspectNativeServices(ctx context.Context) map[leapmuxv1.AgentProvider][]NativeServiceObservation {
	if ctx.Err() != nil {
		return nil
	}
	m.nativeServicesMu.Lock()
	if m.nativeServicesClosed {
		m.nativeServicesMu.Unlock()
		return nil
	}
	if m.nativeServices == nil {
		m.nativeServices = map[leapmuxv1.AgentProvider]NativeServiceObserver{}
	}
	for _, provider := range m.registry.Providers() {
		reg, _ := m.registry.Registration(provider)
		if reg.NewNativeServiceObserver == nil {
			continue
		}
		if _, exists := m.nativeServices[provider]; !exists {
			m.nativeServices[provider] = reg.NewNativeServiceObserver()
		}
	}
	observers := make(map[leapmuxv1.AgentProvider]NativeServiceObserver, len(m.nativeServices))
	for provider, observer := range m.nativeServices {
		observers[provider] = observer
	}
	m.nativeServicesMu.Unlock()
	result := map[leapmuxv1.AgentProvider][]NativeServiceObservation{}
	for provider, observer := range observers {
		if ctx.Err() != nil {
			break
		}
		if observer != nil {
			result[provider] = observer.Inspect(ctx)
		}
	}
	return result
}

func (m *Manager) closeNativeServices() {
	m.nativeServicesMu.Lock()
	if m.nativeServicesClosed {
		m.nativeServicesMu.Unlock()
		return
	}
	m.nativeServicesClosed = true
	observers := m.nativeServices
	m.nativeServices = nil
	m.nativeServicesMu.Unlock()
	for _, observer := range observers {
		if observer != nil {
			_ = observer.Close()
		}
	}
}
