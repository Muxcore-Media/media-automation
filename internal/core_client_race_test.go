package internal

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestStartDialCoreConcurrentWithRequests guards the data race between the
// dialCore goroutine (which assigns the core mesh client) and code paths that
// read it (publish, discovery lookups) and Stop (which closes it).
func TestStartDialCoreConcurrentWithRequests(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lis.Close() }()
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	t.Setenv("MUXCORE_GRPC_ADDR", lis.Addr().String())
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
	t.Setenv("MUXCORE_DEV_TLS_SKIP", "true")

	m := NewModule(Config{
		DBPath:   filepath.Join(t.TempDir(), "race.db"),
		GRPCAddr: "127.0.0.1:0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = m.findModuleByCapability(ctx, "metadata")
				_, _ = m.syncIndexers(ctx)
				_, _, _ = m.findPreferredModule(ctx, "downloader", "")
				m.publishEvent(ctx, "test.event", []byte("{}"))
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()

	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
