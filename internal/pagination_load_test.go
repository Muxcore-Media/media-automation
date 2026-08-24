package internal

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	autov1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
)

// Admin-ui automation page sizes (see admin-ui/handler/automation.go).
const (
	adminQueuePageSize   = 25
	adminHistoryPageSize = 10
)

func TestGetHistoryPagination(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		insertHistoryWithDownloadID(t, m, fmt.Sprintf("hist_%d", i), fmt.Sprintf("dl_%d", i))
	}

	page1, err := m.GetHistory(ctx, &autov1.GetHistoryRequest{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page1.GetTotal() != 5 {
		t.Fatalf("total: got %d want 5", page1.GetTotal())
	}
	if len(page1.GetRecords()) != 2 {
		t.Fatalf("page1 len: got %d want 2", len(page1.GetRecords()))
	}
	if page1.GetPageSize() != 2 {
		t.Fatalf("page_size: got %d want 2", page1.GetPageSize())
	}

	page3, err := m.GetHistory(ctx, &autov1.GetHistoryRequest{Page: 3, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.GetRecords()) != 1 {
		t.Fatalf("page3 len: got %d want 1", len(page3.GetRecords()))
	}
}

func TestPaginationClampsAdminSafe(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.AddToQueue(ctx, &autov1.AddToQueueRequest{ItemType: "movie", ItemId: "m1", Title: "A"})
	insertHistoryWithDownloadID(t, m, "h1", "d1")

	q, err := m.GetQueue(ctx, &autov1.GetQueueRequest{Page: 0, PageSize: 0})
	if err != nil {
		t.Fatal(err)
	}
	if q.GetPage() != 1 || q.GetPageSize() != 20 {
		t.Fatalf("queue defaults: page=%d size=%d want 1/20", q.GetPage(), q.GetPageSize())
	}

	qHuge, err := m.GetQueue(ctx, &autov1.GetQueueRequest{Page: 1, PageSize: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if qHuge.GetPageSize() != 20 {
		t.Fatalf("queue oversize clamp: got %d want 20", qHuge.GetPageSize())
	}

	h, err := m.GetHistory(ctx, &autov1.GetHistoryRequest{Page: -1, PageSize: 500})
	if err != nil {
		t.Fatal(err)
	}
	if h.GetPage() != 1 || h.GetPageSize() != 20 {
		t.Fatalf("history clamp: page=%d size=%d want 1/20", h.GetPage(), h.GetPageSize())
	}
}

// TestQueueHistoryPaginationUnderAdminLoad hammers GetQueue/GetHistory at the
// page sizes the admin-ui automation page uses, verifying stable totals and
// bounded page lengths under concurrent readers.
func TestQueueHistoryPaginationUnderAdminLoad(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	const queueN = 80
	const histN = 55
	for i := 0; i < queueN; i++ {
		_, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
			ItemType: "movie",
			ItemId:   fmt.Sprintf("mv_%d", i),
			TmdbId:   int32(1000 + i),
			Title:    fmt.Sprintf("Movie %d", i),
			Year:     2000,
		})
		if err != nil {
			t.Fatalf("AddToQueue %d: %v", i, err)
		}
	}
	for i := 0; i < histN; i++ {
		insertHistoryWithDownloadID(t, m, fmt.Sprintf("load_hist_%d", i), fmt.Sprintf("load_dl_%d", i))
	}

	wantQueuePages := (queueN + adminQueuePageSize - 1) / adminQueuePageSize
	wantHistPages := (histN + adminHistoryPageSize - 1) / adminHistoryPageSize

	var errs atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	workers := 16
	iters := 40
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				qPage := int32((worker+i)%wantQueuePages) + 1
				hPage := int32((worker+i)%wantHistPages) + 1

				q, err := m.GetQueue(ctx, &autov1.GetQueueRequest{
					Page: qPage, PageSize: adminQueuePageSize,
				})
				if err != nil {
					errs.Add(1)
					t.Errorf("GetQueue: %v", err)
					continue
				}
				if q.GetTotal() != queueN {
					errs.Add(1)
					t.Errorf("queue total=%d want %d", q.GetTotal(), queueN)
				}
				if q.GetPageSize() != adminQueuePageSize {
					errs.Add(1)
					t.Errorf("queue page_size=%d want %d", q.GetPageSize(), adminQueuePageSize)
				}
				if n := len(q.GetItems()); n > adminQueuePageSize || n < 1 {
					errs.Add(1)
					t.Errorf("queue page %d len=%d", qPage, n)
				}

				h, err := m.GetHistory(ctx, &autov1.GetHistoryRequest{
					Page: hPage, PageSize: adminHistoryPageSize,
				})
				if err != nil {
					errs.Add(1)
					t.Errorf("GetHistory: %v", err)
					continue
				}
				if h.GetTotal() != histN {
					errs.Add(1)
					t.Errorf("history total=%d want %d", h.GetTotal(), histN)
				}
				if h.GetPageSize() != adminHistoryPageSize {
					errs.Add(1)
					t.Errorf("history page_size=%d want %d", h.GetPageSize(), adminHistoryPageSize)
				}
				if n := len(h.GetRecords()); n > adminHistoryPageSize || n < 1 {
					errs.Add(1)
					t.Errorf("history page %d len=%d", hPage, n)
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	if errs.Load() != 0 {
		t.Fatalf("concurrent pagination had %d errors in %v", errs.Load(), elapsed)
	}
	// Soft budget: admin page has an 8s deadline; this offline load should be far under.
	if elapsed > 5*time.Second {
		t.Fatalf("admin-load pagination took %v (want <5s)", elapsed)
	}
	t.Logf("admin-load pagination ok: workers=%d iters=%d elapsed=%v", workers, iters, elapsed)
}
