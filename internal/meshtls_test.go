package internal

import (
	"context"
	"testing"
)

func TestGRPCServerMeshTLS(t *testing.T) {
	cfg := meshTLSFixture(t)
	m := NewModule(Config{
		DBPath:   t.TempDir() + "/automation.db",
		GRPCAddr: "127.0.0.1:0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })
	assertMeshTLS(t, m.grpcLis.Addr().String(), cfg)
}
