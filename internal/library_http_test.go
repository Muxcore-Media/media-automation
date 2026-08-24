package internal

import "testing"

func TestLibraryCompanionHTTPBase(t *testing.T) {
	got, err := libraryCompanionHTTPBase("127.0.0.1:9650")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:9651" {
		t.Fatalf("got %q", got)
	}
}
