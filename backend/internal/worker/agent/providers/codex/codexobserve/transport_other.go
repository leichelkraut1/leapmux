//go:build !windows && !darwin

package codexobserve

import (
	"context"
	"net"
)

// Connect never creates a router or substitutes a process-only observation.
func Connect(ctx context.Context, _ string) (net.Conn, PeerIdentity, error) {
	if err := ctx.Err(); err != nil {
		return nil, PeerIdentity{}, err
	}
	return nil, PeerIdentity{}, errPeer
}

// VerifyStatePath fails closed until this platform has peer authentication.
func VerifyStatePath(string) error { return errPeer }
