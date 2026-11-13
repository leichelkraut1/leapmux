package channel

import "context"

type agentSocketKey struct{}

// WithAgentSocket marks a call that arrived through a local control socket:
// an agent's or a terminal's `leapmux control`, never the browser. The caller
// of such a call holds the owner's unscoped grant, so a handler that must be
// a person's own act (confirming a live order) refuses it by this mark.
func WithAgentSocket(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentSocketKey{}, true)
}

// ViaAgentSocket reports whether ctx carries the mark.
func ViaAgentSocket(ctx context.Context) bool {
	v, _ := ctx.Value(agentSocketKey{}).(bool)
	return v
}
