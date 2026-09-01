package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	musicv1 "github.com/Muxcore-Media/media-music/proto/gen/muxcore/music/v1"
	"google.golang.org/grpc"
)

type fakeMusicClient struct {
	musicv1.MusicManagementServiceClient
	items []*musicv1.MissingAlbumItem
}

func (f *fakeMusicClient) ListMissing(ctx context.Context, in *musicv1.ListMissingRequest, opts ...grpc.CallOption) (*musicv1.ListMissingResponse, error) {
	_ = ctx
	_ = opts
	page := in.GetPage()
	if page < 1 {
		page = 1
	}
	pageSize := in.GetPageSize()
	if pageSize < 1 {
		pageSize = 100
	}
	start := int((page - 1) * pageSize)
	if start >= len(f.items) {
		return &musicv1.ListMissingResponse{Page: page, PageSize: pageSize, Total: int32(len(f.items))}, nil
	}
	end := start + int(pageSize)
	if end > len(f.items) {
		end = len(f.items)
	}
	return &musicv1.ListMissingResponse{
		Items:    f.items[start:end],
		Total:    int32(len(f.items)),
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func TestSyncWantedBooksComicsAudiobooks(t *testing.T) {
	booksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/missing" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(missingBooksResponse{
			Items: []missingBookItem{{
				BookID: "bk1", AuthorID: "au1", Title: "Dune", AuthorName: "Herbert", Year: 1965,
			}},
			Total: 1, Page: 1, PageSize: 100,
		})
	}))
	t.Cleanup(booksSrv.Close)

	comicsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/missing" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(missingComicsResponse{
			Items: []missingComicItem{{
				IssueID: "is1", SeriesID: "ser1", Title: "Watchmen", Number: "12", Year: 1987, SeriesName: "Watchmen",
			}},
			Total: 1, Page: 1, PageSize: 100,
		})
	}))
	t.Cleanup(comicsSrv.Close)

	audioSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/missing" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(missingAudiobooksResponse{
			Items: []missingAudiobookItem{{
				AudiobookID: "ab1", AuthorID: "au2", Title: "Neuromancer", AuthorName: "Gibson", Year: 1984,
			}},
			Total: 1, Page: 1, PageSize: 100,
		})
	}))
	t.Cleanup(audioSrv.Close)

	m := newTestModule(t)
	m.testLibraryHTTP = map[string]string{
		"media.books":      booksSrv.URL,
		"media.comics":     comicsSrv.URL,
		"media.audiobooks": audioSrv.URL,
	}
	ctx := context.Background()
	seen := make(map[string]struct{})

	if n, err := m.syncWantedBooks(ctx, seen); err != nil || n != 1 {
		t.Fatalf("books: n=%d err=%v", n, err)
	}
	if n, err := m.syncWantedComics(ctx, seen); err != nil || n != 1 {
		t.Fatalf("comics: n=%d err=%v", n, err)
	}
	if n, err := m.syncWantedAudiobooks(ctx, seen); err != nil || n != 1 {
		t.Fatalf("audiobooks: n=%d err=%v", n, err)
	}

	var issueNumber string
	m.mu.RLock()
	err := m.db.QueryRow(`SELECT issue_number FROM wanted_items WHERE item_type = 'comic' AND item_id = 'is1'`).Scan(&issueNumber)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if issueNumber != "12" {
		t.Fatalf("issue_number=%q want 12", issueNumber)
	}
	if _, ok := seen["book:bk1"]; !ok {
		t.Fatal("book not seen")
	}
	if _, ok := seen["comic:is1"]; !ok {
		t.Fatal("comic not seen")
	}
	if _, ok := seen["audiobook:ab1"]; !ok {
		t.Fatal("audiobook not seen")
	}
}

func TestSyncWantedMusic(t *testing.T) {
	m := newTestModule(t)
	m.musicClient = &fakeMusicClient{items: []*musicv1.MissingAlbumItem{{
		AlbumId: "al1", ArtistId: "ar1", ArtistName: "Radiohead", Title: "OK Computer", Year: 1997,
	}}}
	ctx := context.Background()
	seen := make(map[string]struct{})
	n, err := m.syncWantedMusic(ctx, seen)
	if err != nil || n != 1 {
		t.Fatalf("music: n=%d err=%v", n, err)
	}
	if _, ok := seen["music:al1"]; !ok {
		t.Fatal("music album not seen")
	}
}

func TestPruneWantedNotInLibrariesBooksOnly(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	now := "2026-01-01T00:00:00Z"
	m.mu.Lock()
	_, err := m.db.Exec(`
		INSERT INTO wanted_items (id, item_type, item_id, tmdb_id, title, monitored, missing, created_at, updated_at)
		VALUES ('w_book_keep', 'book', 'keep', 0, 'Keep', 1, 1, ?, ?),
		       ('w_book_drop', 'book', 'drop', 0, 'Drop', 1, 1, ?, ?),
		       ('w_movie_stale', 'movie', 'mv1', 1, 'Movie', 1, 1, ?, ?)`,
		now, now, now, now, now, now)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]struct{}{"book:keep": {}}
	m.pruneWantedNotInLibraries(ctx, seen, false, false, false, true, false, false)

	m.mu.RLock()
	defer m.mu.RUnlock()
	var n int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("count=%d want 2 (keep book + untouched movie)", n)
	}
	var dropExists int
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items WHERE item_id = 'drop'`).Scan(&dropExists)
	if dropExists != 0 {
		t.Fatal("pruned book should be deleted")
	}
}
