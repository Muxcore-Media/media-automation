package internal

import (
	"context"
	"os"
	"testing"

	autov1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
)

func TestValidateDispatchGrab_RejectsLiveIndexerInFixtureMode(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "fixture")
	err := validateDispatchGrab(&autov1.DispatchRequest{
		Title:       "Steel Magnolias",
		IndexerName: "Torznab",
		Guid:        "https://example.com/release",
		DownloadUrl: "http://127.0.0.1:9696/download",
	})
	if err == nil {
		t.Fatal("expected error for live grab with fixture downloader")
	}
}

func TestValidateDispatchGrab_AllowsFixtureIndexer(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "fixture")
	if err := validateDispatchGrab(&autov1.DispatchRequest{
		Title:       "Fight Club",
		IndexerName: "fixture-indexer",
		Guid:        "guid-fight-club-fixture",
		DownloadUrl: "magnet:?xt=urn:btih:fixturefightclub1999",
	}); err != nil {
		t.Fatalf("fixture grab should be allowed: %v", err)
	}
}

func TestDispatch_RejectsLiveGrabInFixtureMode(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "fixture")
	m := newTestModule(t)
	m.downloaderClient = &fakeDownloaderClient{torrentID: "should-not-run"}

	_, err := m.Dispatch(context.Background(), &autov1.DispatchRequest{
		Title:            "Steel Magnolias",
		IndexerName:      "Torznab",
		Guid:             "https://knaben.xyz/thepiratebay/description.php?id=1",
		DownloadUrl:      "http://127.0.0.1:9696/2/download?apikey=x",
		DownloadProtocol: "torrent",
		ItemId:           "tmdb_10860",
		ItemType:         "movie",
	})
	if err == nil {
		t.Fatal("expected dispatch rejection")
	}
}

func TestValidateDispatchGrab_LiveDownloaderAllowsTorznab(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "live")
	t.Setenv("QBIT_FIXTURE", "")
	if err := validateDispatchGrab(&autov1.DispatchRequest{
		Title:       "Steel Magnolias",
		IndexerName: "Torznab",
	}); err != nil {
		t.Fatalf("live downloader should allow: %v", err)
	}
}

func TestFixtureDownloaderConfigured_Table(t *testing.T) {
	cases := []struct {
		name   string
		engine *string
		qbit   string
		want   bool
	}{
		{"unset", nil, "", true},
		{"empty", ptr(""), "", true},
		{"whitespace", ptr("  "), "", true},
		{"fixture", ptr("fixture"), "", true},
		{"fake", ptr("FAKE"), "", true},
		{"live", ptr("live"), "", false},
		{"live-mixed-case", ptr(" Live "), "", false},
		{"anacrolix-alias", ptr("anacrolix"), "", false},
		{"unknown", ptr("bogus"), "", true},
		{"live-but-qbit-fixture", ptr("live"), "1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("DOWNLOADER_ENGINE", "")
			t.Setenv("QBIT_FIXTURE", c.qbit)
			if c.engine == nil {
				_ = os.Unsetenv("DOWNLOADER_ENGINE")
			} else {
				t.Setenv("DOWNLOADER_ENGINE", *c.engine)
			}
			if got := fixtureDownloaderConfigured(); got != c.want {
				t.Fatalf("guard on = %v, want %v", got, c.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestDispatch_RefusesLiveGrabWhenEngineUnset(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "")
	_ = os.Unsetenv("DOWNLOADER_ENGINE")
	t.Setenv("QBIT_FIXTURE", "")
	m := newTestModule(t)
	m.downloaderClient = &fakeDownloaderClient{torrentID: "should-not-run"}
	_, err := m.Dispatch(context.Background(), &autov1.DispatchRequest{
		Title: "Steel Magnolias", IndexerName: "Torznab",
		Guid: "https://example.com/1", DownloadUrl: "http://127.0.0.1:9696/2/download",
		DownloadProtocol: "torrent", ItemId: "tmdb_10860", ItemType: "movie",
	})
	if err == nil {
		t.Fatal("expected dispatch refusal with DOWNLOADER_ENGINE unset")
	}
}

func TestValidateDispatchGrab_LiveEngineAllowsNonFixture(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "live")
	t.Setenv("QBIT_FIXTURE", "")
	if err := validateDispatchGrab(&autov1.DispatchRequest{Title: "x", IndexerName: "Torznab"}); err != nil {
		t.Fatalf("live engine should allow: %v", err)
	}
}
