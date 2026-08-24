package internal

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	autov1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
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

func insertSeasonZeroDummy(t *testing.T, m *Module, itemID, seriesID string, episode int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.db.Exec(`INSERT INTO wanted_items (id, item_type, item_id, tmdb_id, title, year, season_number, episode_number, monitored, missing, series_id, created_at, updated_at)
		VALUES (?, 'tv', ?, 253, 'Star Trek', 1966, 0, ?, 1, 1, ?, ?, ?)`,
		"w_tv_"+itemID, itemID, episode, seriesID, now, now)
	if err != nil {
		t.Fatal(err)
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
	insertSeasonZeroDummy(t, m, "ep_tos_0_12", "tv_tos", 12)
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

func TestSearchAndStoreSkipsWrongYearEpisodeForPack(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "tv_franklin_s5", TmdbID: 908, Title: "Franklin", Year: 1997,
		SeasonNumber: 5, EpisodeNumber: 0, SeriesID: "tv_franklin",
		CleanTitles: []string{cleanMatchTitle("Franklin")},
	})
	var wantedID string
	m.mu.RLock()
	_ = m.db.QueryRow(`SELECT id FROM wanted_items WHERE item_id = 'tv_franklin_s5'`).Scan(&wantedID)
	m.mu.RUnlock()

	fake := &fakeDownloaderClient{torrentID: "tor-should-not-add"}
	m.downloaderClient = fake
	idx := &countingIndexer{fakeIndexerClient: fakeIndexerClient{
		results: []*indexerv1.SearchResult{{
			Guid:        "g-franklin-2025",
			Title:       "Franklin-2025-S01E02 1080p WEB",
			DownloadUrl: "magnet:?xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff",
		}},
	}}
	m.testIndexerClients = map[string]indexerv1.IndexerServiceClient{"idx": idx}

	m.searchAndStore(ctx, wantedID, "tv", "tv_franklin_s5", "Franklin", 908, 1997, 5, 0, 0, "", "tv_franklin", "", []string{cleanMatchTitle("Franklin")}, true, 0, "")
	if fake.calls != 0 {
		t.Fatalf("wrong-year episode must not AddTorrent, calls=%d", fake.calls)
	}
	if m.isBlacklisted(ctx, "tv_franklin_s5", "g-franklin-2025", 1) {
		t.Fatal("skipped mismatch must not consume the attempt loop")
	}
}

func TestCoerceTVWantedGrain(t *testing.T) {
	s, e, id := coerceTVWantedGrain("tv", 0, 12, "ep_tos_0_12", "tv_tos")
	if s != 0 || e != 0 || id != "tv_tos" {
		t.Fatalf("dummy: season=%d episode=%d id=%s", s, e, id)
	}
	s, e, id = coerceTVWantedGrain("tv", 0, 0, "tv_tos", "tv_tos")
	if s != 0 || e != 0 || id != "tv_tos" {
		t.Fatalf("pack: season=%d episode=%d id=%s", s, e, id)
	}
	s, e, id = coerceTVWantedGrain("tv", 3, 24, "ep_s03e24", "tv_tos")
	if s != 3 || e != 24 || id != "ep_s03e24" {
		t.Fatalf("episode: season=%d episode=%d id=%s", s, e, id)
	}
}

func TestEpisodeGrainSkipsFullSeriesRemux(t *testing.T) {
	t.Parallel()
	title := "Breaking Bad (2008) S01-S05 1080p BluRay REMUX Dual Audio [Hindi+Eng] ~ RemuxDoc"
	scored := []scoredRelease{{Title: title, Size: 40 << 30, Score: 500, DownloadURL: "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	got := filterTVReleaseGrain(scored, "tv", "Breaking Bad", 2008, 1, 1, 0, "standard", nil)
	if len(got) != 0 {
		t.Fatalf("episode wanted kept %d hits, want 0", len(got))
	}
}

func TestPackGrainKeepsSeriesRemuxUnderCap(t *testing.T) {
	t.Parallel()
	title := "Breaking Bad (2008) S01-S05 1080p BluRay REMUX Dual Audio [Hindi+Eng] ~ RemuxDoc"
	under := scoredRelease{Title: title, Size: 40 << 30, Score: 200, DownloadURL: "magnet:a"}
	over := scoredRelease{Title: title, Size: 300 << 30, Score: 400, DownloadURL: "magnet:b"}
	got := filterTVReleaseGrain([]scoredRelease{under, over}, "tv", "Breaking Bad", 2008, 1, 0, 0, "standard", nil)
	if len(got) != 2 {
		t.Fatalf("pack grain kept %d want 2", len(got))
	}
	capped := filterOversizedReleases(got, int64(defaultMaxReleaseGiB)<<30)
	if len(capped) != 1 || capped[0].Size != under.Size {
		t.Fatalf("size cap kept %+v", capped)
	}
}

func TestDispatchRejectsOversizedRelease(t *testing.T) {
	m := newTestModule(t)
	fake := &fakeDownloaderClient{torrentID: "tor-huge"}
	m.downloaderClient = fake
	_, err := m.Dispatch(context.Background(), &autov1.DispatchRequest{
		Guid:        "guid-huge",
		Title:       "Breaking Bad (2008) S01-S05 1080p BluRay REMUX",
		DownloadUrl: "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:        300 << 30,
		ItemType:    "tv",
		ItemId:      "tv_bb",
	})
	if err == nil {
		t.Fatal("expected size cap error")
	}
	if fake.calls != 0 {
		t.Fatalf("AddTorrent calls=%d want 0", fake.calls)
	}
}
