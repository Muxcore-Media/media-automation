package internal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLibraryPlusImportRoute(t *testing.T) {
	cap, path, ok := libraryPlusImportRoute("book", "bk1")
	if !ok || cap != "media.books" || path != "/api/books/bk1/import" {
		t.Fatalf("book route %q %q %v", cap, path, ok)
	}
	cap, path, ok = libraryPlusImportRoute("comic", "iss/1")
	if !ok || cap != "media.comics" || path != "/api/issues/iss%2F1/import" {
		t.Fatalf("comic route %q %q %v", cap, path, ok)
	}
	cap, path, ok = libraryPlusImportRoute("audiobook", "ab1")
	if !ok || cap != "media.audiobooks" || path != "/api/audiobooks/ab1/import" {
		t.Fatalf("audiobook route %q %q %v", cap, path, ok)
	}
	if _, _, ok := libraryPlusImportRoute("movie", "m1"); ok {
		t.Fatal("movie should use scanner")
	}
}

func TestPostLibraryImport(t *testing.T) {
	var gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(up.Close)
	if err := postLibraryImport(context.Background(), up.URL, "/api/books/bk1/import", "/data/book.epub"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/books/bk1/import" {
		t.Fatalf("path %s", gotPath)
	}
	if !json.Valid([]byte(gotBody)) || gotBody == "" {
		t.Fatalf("body %s", gotBody)
	}
}
