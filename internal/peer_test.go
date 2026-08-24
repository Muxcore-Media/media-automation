package internal

import (
	"context"
	"testing"
	"time"
)

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
