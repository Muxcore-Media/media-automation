package internal

import (
	"context"
	"testing"
	"time"
)

func TestMeshInsecureEnv(t *testing.T) {
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
	if !meshInsecure() {
		t.Fatal("expected insecure when MUXCORE_INSECURE_DISABLE_TLS=true")
	}
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "false")
	t.Setenv("MUXCORE_GRPC_INSECURE", "true")
	if !meshInsecure() {
		t.Fatal("expected insecure when MUXCORE_GRPC_INSECURE=true")
	}
}

func TestWithPeerTimeoutAddsDeadline(t *testing.T) {
	ctx, cancel := withPeerTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline on peer context")
	}
	if time.Until(deadline) <= 0 || time.Until(deadline) > 50*time.Millisecond {
		t.Fatalf("unexpected deadline: %v", deadline)
	}
}

func TestWithPeerTimeoutPreservesCallerDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), time.Second)
	defer parentCancel()
	ctx, cancel := withPeerTimeout(parent, 50*time.Millisecond)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	ctxDeadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	if !ctxDeadline.Equal(parentDeadline) {
		t.Fatalf("deadline changed: parent=%v ctx=%v", parentDeadline, ctxDeadline)
	}
}
