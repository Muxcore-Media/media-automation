package internal

import (
	"context"
	"time"
)

const peerDiscoveryTimeout = 8 * time.Second

// withPeerTimeout applies a default deadline when the caller did not set one.
func withPeerTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
