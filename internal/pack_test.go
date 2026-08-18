package internal

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	indexerv1 "github.com/Muxcore-Media/contracts-indexer/muxcore/indexer/v1"
	"google.golang.org/grpc"
)

func TestPackSeasonsCovered(t *testing.T) {
	cases := []struct {
		title    string
		lo, hi   int
		ok       bool
		covers   []int
		uncovers []int
	}{
		{
			title: "King.of.the.Hill.S15.Complete.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir",
			lo:    15, hi: 15, ok: true,
			covers:   []int{15},
			uncovers: []int{14, 16},
		},
		{
			title: "Breaking Bad (2008) S01-S05 1080p BluRay REMUX Dual Audio [Hindi+Eng] ~ RemuxDoc",
			lo:    1, hi: 5, ok: true,
			covers:   []int{1, 4, 5},
			uncovers: []int{6},
		},
		{
			title: "Show.S01E01-E13.1080p.WEB",
			lo:    1, hi: 1, ok: true,
			covers:   []int{1},
			uncovers: []int{2},
		},
		{
			title:    "Arthur.S21E02.HDTV.x264-W4F",
			ok:       false,
			uncovers: []int{21},
		},
		{
			title:    "Last Man Standing 2011 S07E08 HRs Rough n Stuff 1080p DSNP WEB-DL",
			ok:       false,
			uncovers: []int{7},
		},
		{
			title: "Season 03 Complete WEBRip",
			lo:    3, hi: 3, ok: true,
			covers: []int{3},
		},
		{
			title: "Marvel's Agents of S H I E L D 2013 S05 1080p BDrip x265",
			lo:    5, hi: 5, ok: true,
			covers:   []int{5},
			uncovers: []int{1, 4},
		},
		{
			title: "The Pink Panther S01   S04 432p DVDRip Xvid MTN",
			lo:    1, hi: 4, ok: true,
			covers:   []int{1, 2, 4},
			uncovers: []int{5},
		},
	}
	for _, tc := range cases {
		lo, hi, ok := packSeasonsCovered(tc.title)
		if ok != tc.ok || lo != tc.lo || hi != tc.hi {
			t.Errorf("%q: got lo=%d hi=%d ok=%v want lo=%d hi=%d ok=%v", tc.title, lo, hi, ok, tc.lo, tc.hi, tc.ok)
		}
		for _, s := range tc.covers {
			if !packCoversSeason(tc.title, s) {
				t.Errorf("%q should cover season %d", tc.title, s)
			}
		}
		for _, s := range tc.uncovers {
			if packCoversSeason(tc.title, s) {
				t.Errorf("%q should not cover season %d", tc.title, s)
			}
		}
	}
}

func insertGrabbedPack(t *testing.T, m *Module, itemID, seriesID, histTitle, status string, season int32) {
	t.Helper()
	ctx := context.Background()
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: itemID, TmdbID: 2122, Title: "King of the Hill", Year: 1997,
		SeasonNumber: season, EpisodeNumber: 1, SeriesID: seriesID,
	})
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id)
		VALUES (?, ?, ?, ?, '', 0, 145, 'magnet:?xt=urn:btih:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', 'torrent', ?, ?, ?, ?)`,
		"dl_"+itemID, itemID, "guid_"+itemID, histTitle, status, now, now, "tor_"+itemID)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSeasonPackAlreadyGrabbedRequiresSeries(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	title := "King.of.the.Hill.S15.Complete.1080p.WEBRip"
	insertGrabbedPack(t, m, "ep_s15e01", "tv_koth", title, "completed", 15)

	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep_s15e02", TmdbID: 2122, Title: "King of the Hill", Year: 1997,
		SeasonNumber: 15, EpisodeNumber: 2, SeriesID: "tv_koth",
	})
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep_s14e01", TmdbID: 2122, Title: "King of the Hill", Year: 1997,
		SeasonNumber: 14, EpisodeNumber: 1, SeriesID: "tv_koth",
	})
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep_other_s15", TmdbID: 99, Title: "Other Show", Year: 1999,
		SeasonNumber: 15, EpisodeNumber: 1, SeriesID: "tv_other",
	})

	if !m.seasonPackAlreadyGrabbed(ctx, "tv_koth", 15) {
		t.Fatal("S15 sibling should be covered")
	}
	if m.seasonPackAlreadyGrabbed(ctx, "tv_koth", 14) {
		t.Fatal("S14 should still search")
	}
	if m.seasonPackAlreadyGrabbed(ctx, "tv_other", 15) {
		t.Fatal("unrelated series must not match")
	}
	if m.seasonPackAlreadyGrabbed(ctx, "", 15) {
		t.Fatal("empty series_id must not match")
	}
}

func TestSeasonPackSentAlsoCovers(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	insertGrabbedPack(t, m, "ep_bb_s01", "tv_bb", "Breaking Bad (2008) S01-S05 1080p BluRay REMUX", "sent", 1)
	if !m.seasonPackAlreadyGrabbed(ctx, "tv_bb", 4) {
		t.Fatal("S01-S05 sent pack should cover S04")
	}
}

func TestSkipSeasonZeroPlaceholderWhenSeriesGrabbing(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	insertGrabbedPack(t, m, "ep_tos_s03e24", "tv_tos", "Star Trek S03E24 Turnabout Intruder 1080p", "import_failed", 3)
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep_tos_0_12", TmdbID: 253, Title: "Star Trek", Year: 1966,
		SeasonNumber: 0, EpisodeNumber: 12, SeriesID: "tv_tos",
	})
	grabbing := m.seriesWithGrabs(ctx)
	if !skipSeasonZeroPlaceholder("tv", 0, 12, "tv_tos", grabbing) {
		t.Fatal("season-0 dummy should skip once the series is grabbing (including import_failed)")
	}
	if skipSeasonZeroPlaceholder("tv", 0, 0, "tv_tos", grabbing) {
		t.Fatal("season-0 pack row (episode 0) should still search")
	}
	if skipSeasonZeroPlaceholder("tv", 3, 1, "tv_tos", grabbing) {
		t.Fatal("real S03E01 should still search")
	}
	if !skipSeasonZeroPlaceholder("tv", 0, 12, "tv_other", nil) {
		t.Fatal("season-0 dummy should skip even with no grabbing series")
	}

	var wantedID string
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT id FROM wanted_items WHERE item_id = 'ep_tos_0_12'`).Scan(&wantedID)
	m.mu.RUnlock()
	idx := &countingIndexer{fakeIndexerClient: fakeIndexerClient{
		results: []*indexerv1.SearchResult{{Guid: "g", Title: "Star Trek S03E24", DownloadUrl: "magnet:?xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff"}},
	}}
	m.testIndexerClients = map[string]indexerv1.IndexerServiceClient{"idx": idx}
	m.searchAndStore(ctx, wantedID, "tv", "ep_tos_0_12", "Star Trek", 253, 1966, 0, 12, 0, "", "tv_tos", "", nil, true, 0, "")
	if idx.n.Load() != 0 {
		t.Fatalf("indexer searches=%d want 0", idx.n.Load())
	}
}

func TestSingleEpisodeDoesNotCoverSiblings(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	insertGrabbedPack(t, m, "ep_arthur_s21e02", "tv_arthur", "Arthur.S21E02.HDTV.x264-W4F", "completed", 21)
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep_arthur_s21e03", TmdbID: 1, Title: "Arthur",
		SeasonNumber: 21, EpisodeNumber: 3, SeriesID: "tv_arthur",
	})
	if m.seasonPackAlreadyGrabbed(ctx, "tv_arthur", 21) {
		t.Fatal("single episode must not cover the season")
	}
}

type countingIndexer struct {
	fakeIndexerClient
	n atomic.Int32
}

func (c *countingIndexer) Search(ctx context.Context, in *indexerv1.SearchRequest, opts ...grpc.CallOption) (*indexerv1.SearchResponse, error) {
	c.n.Add(1)
	return c.fakeIndexerClient.Search(ctx, in, opts...)
}

func TestSearchAndStoreSkipsCoveredSeason(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	title := "King.of.the.Hill.S15.Complete.1080p.WEBRip"
	insertGrabbedPack(t, m, "ep_s15e01", "tv_koth", title, "completed", 15)
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep_s15e02", TmdbID: 2122, Title: "King of the Hill", Year: 1997,
		SeasonNumber: 15, EpisodeNumber: 2, SeriesID: "tv_koth",
	})
	var wantedID string
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT id FROM wanted_items WHERE item_id = 'ep_s15e02'`).Scan(&wantedID)
	m.mu.RUnlock()

	idx := &countingIndexer{fakeIndexerClient: fakeIndexerClient{
		results: []*indexerv1.SearchResult{{Guid: "g", Title: title, DownloadUrl: "magnet:?xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff"}},
	}}
	m.testIndexerClients = map[string]indexerv1.IndexerServiceClient{"idx": idx}

	m.searchAndStore(ctx, wantedID, "tv", "ep_s15e02", "King of the Hill", 2122, 1997, 15, 2, 0, "", "tv_koth", "", nil, true, 0, "")
	if idx.n.Load() != 0 {
		t.Fatalf("indexer searches=%d want 0", idx.n.Load())
	}
}

func TestSearchAndStoreStillSearchesOtherSeason(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	insertGrabbedPack(t, m, "ep_s15e01", "tv_koth", "King.of.the.Hill.S15.Complete.1080p.WEBRip", "completed", 15)
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep_s14e01", TmdbID: 2122, Title: "King of the Hill", Year: 1997,
		SeasonNumber: 14, EpisodeNumber: 1, SeriesID: "tv_koth",
	})
	var wantedID string
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT id FROM wanted_items WHERE item_id = 'ep_s14e01'`).Scan(&wantedID)
	m.mu.RUnlock()

	idx := &countingIndexer{fakeIndexerClient: fakeIndexerClient{
		results: []*indexerv1.SearchResult{{Guid: "g", Title: "King.of.the.Hill.S14E01.WEB", DownloadUrl: "magnet:?xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff"}},
	}}
	m.testIndexerClients = map[string]indexerv1.IndexerServiceClient{"idx": idx}

	m.searchAndStore(ctx, wantedID, "tv", "ep_s14e01", "King of the Hill", 2122, 1997, 14, 1, 0, "", "tv_koth", "", nil, true, 0, "")
	if idx.n.Load() == 0 {
		t.Fatal("S14 should still search the indexer")
	}
}
