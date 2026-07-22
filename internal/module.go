package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	automationv1 "github.com/Muxcore-Media/media-automation/proto/automationv1"

	indexerv1 "github.com/Muxcore-Media/contracts-indexer/muxcore/indexer/v1"
	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
	formatsv1 "github.com/Muxcore-Media/media-custom-formats/proto/formatsv1"
	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"

	"github.com/Muxcore-Media/core/pkg/contracts"
	eventsv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/events/v1"
	"github.com/Muxcore-Media/core/sdk/go/client"
	_ "modernc.org/sqlite"
)

type Module struct {
	automationv1.UnimplementedAutomationServiceServer

	mu sync.RWMutex
	db *sql.DB
	mc *client.Client

	id       string
	dbPath   string
	grpcAddr string
	grpcSrv  *grpc.Server
	grpcLis  net.Listener

	indexerConn      *grpc.ClientConn
	indexerClient    indexerv1.IndexerServiceClient
	downloaderConn   *grpc.ClientConn
	downloaderClient downloaderv1.TorrentServiceClient
	formatsConn      *grpc.ClientConn
	formatsClient    formatsv1.FormatServiceClient
	moviesConn       *grpc.ClientConn
	moviesClient     mgmntv1.MovieManagementServiceClient
	tvConn           *grpc.ClientConn
	tvClient         tvmgmtv1.TvManagementServiceClient
}

type Config struct {
	ID       string
	DBPath   string
	GRPCAddr string
}

func NewModule(cfg Config) *Module {
	if cfg.ID == "" {
		cfg.ID = "media-automation"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "/var/lib/media-automation/automation.db"
	}
	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = ":9460"
	}
	if v := os.Getenv("AUTOMATION_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("AUTOMATION_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}
	return &Module{
		id:       cfg.ID,
		dbPath:   cfg.DBPath,
		grpcAddr: cfg.GRPCAddr,
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:             m.id,
		Name:           "Media Automation",
		Version:        "0.1.0",
		Roles:          []string{"automation"},
		Description:    "Automation engine — searches searchers, scores releases, and dispatches downloads for wanted media",
		Author:         "MuxCore",
		Capabilities:   []string{"media.automation"},
		MinCoreVersion: "0.4.0",
		HTTPAddr:       m.grpcAddr,
	}
}

func (m *Module) Init(ctx context.Context) error {
	dir := fileDir(m.dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", m.dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS wanted_items (
			id             TEXT PRIMARY KEY,
			item_type      TEXT NOT NULL,
			item_id        TEXT NOT NULL,
			tmdb_id        INTEGER NOT NULL,
			title          TEXT NOT NULL,
			year           INTEGER DEFAULT 0,
			season_number  INTEGER DEFAULT 0,
			episode_number INTEGER DEFAULT 0,
			monitored      INTEGER DEFAULT 1,
			missing        INTEGER DEFAULT 1,
			quality_profile_id TEXT DEFAULT '',
			last_searched  TEXT,
			created_at     TEXT NOT NULL,
			updated_at     TEXT NOT NULL,
			UNIQUE(item_type, item_id)
		)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create wanted_items table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE wanted_items ADD COLUMN quality_profile_id TEXT DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return fmt.Errorf("migrate wanted_items: %w", err)
	}
	for _, col := range []string{
		`ALTER TABLE wanted_items ADD COLUMN absolute_number INTEGER DEFAULT 0`,
		`ALTER TABLE wanted_items ADD COLUMN series_type TEXT DEFAULT ''`,
		`ALTER TABLE wanted_items ADD COLUMN series_id TEXT DEFAULT ''`,
	} {
		if _, err := db.ExecContext(ctx, col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return fmt.Errorf("migrate wanted_items: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS download_history (
			id              TEXT PRIMARY KEY,
			wanted_item_id  TEXT NOT NULL,
			guid            TEXT NOT NULL,
			title           TEXT NOT NULL,
			indexer         TEXT DEFAULT '',
			size            INTEGER DEFAULT 0,
			score           INTEGER DEFAULT 0,
			download_url    TEXT DEFAULT '',
			download_protocol TEXT DEFAULT '',
			status          TEXT DEFAULT 'pending',
			sent_at         TEXT,
			completed_at    TEXT,
			created_at      TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create download_history table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_wanted_type ON wanted_items(item_type, missing)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create wanted index: %w", err)
	}

	m.mu.Lock()
	m.db = db
	m.mu.Unlock()

	lis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		db.Close()
		return fmt.Errorf("listen gRPC %s: %w", m.grpcAddr, err)
	}
	m.grpcLis = lis

	slog.Info("media-automation initialized",
		"db", m.dbPath,
		"grpc", m.grpcAddr,
	)
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	m.grpcSrv = grpc.NewServer()
	automationv1.RegisterAutomationServiceServer(m.grpcSrv, m)

	go func() {
		slog.Info("media-automation gRPC service started", "addr", m.grpcAddr)
		if err := m.grpcSrv.Serve(m.grpcLis); err != nil {
			slog.Error("media-automation gRPC serve error", "error", err)
		}
	}()

	go m.dialCore(context.Background())
	go m.rssSyncLoop()
	go m.subscribeToMediaEvents()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	if m.mc != nil {
		m.mc.Close()
	}
	if m.indexerConn != nil {
		m.indexerConn.Close()
	}
	if m.downloaderConn != nil {
		m.downloaderConn.Close()
	}
	if m.formatsConn != nil {
		m.formatsConn.Close()
	}
	if m.moviesConn != nil {
		m.moviesConn.Close()
	}
	if m.tvConn != nil {
		m.tvConn.Close()
	}
	m.mu.Lock()
	if m.db != nil {
		m.db.Close()
		m.db = nil
	}
	m.mu.Unlock()
	slog.Info("media-automation stopped")
	return nil
}

func (m *Module) Health(ctx context.Context) error {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return fmt.Errorf("not initialized")
	}
	return db.PingContext(ctx)
}

// ── Core connection ────────────────────────────────────────────

func (m *Module) dialCore(ctx context.Context) {
	meshAddr := os.Getenv("MUXCORE_GRPC_ADDR")
	if meshAddr == "" {
		meshAddr = "localhost:9090"
	}
	insecureMode := os.Getenv("MUXCORE_GRPC_INSECURE") == "true"

	var opts []client.Option
	if insecureMode {
		opts = append(opts, client.WithInsecure())
	}

	c, err := client.Dial(meshAddr, opts...)
	if err != nil {
		slog.Error("media-automation: dial core", "error", err)
		return
	}
	m.mc = c
	slog.Info("media-automation: connected to core mesh", "addr", meshAddr)
}

func (m *Module) findModuleByCapability(ctx context.Context, cap string) (string, error) {
	if m.mc == nil {
		return "", fmt.Errorf("not connected to core")
	}
	modules, err := m.mc.Discovery.FindByCapability(ctx, cap)
	if err != nil {
		return "", fmt.Errorf("discover %s: %w", cap, err)
	}
	for _, mod := range modules {
		if mod.HttpAddr != "" {
			return mod.HttpAddr, nil
		}
	}
	return "", fmt.Errorf("no %s module found", cap)
}

// ── Module connections ─────────────────────────────────────────

func (m *Module) ensureIndexer(ctx context.Context) error {
	m.mu.RLock()
	if m.indexerClient != nil {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()

	addr, err := m.findModuleByCapability(ctx, "indexer")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial indexer: %w", err)
	}
	m.mu.Lock()
	m.indexerConn = conn
	m.indexerClient = indexerv1.NewIndexerServiceClient(conn)
	m.mu.Unlock()
	return nil
}

func (m *Module) ensureDownloader(ctx context.Context) error {
	m.mu.RLock()
	if m.downloaderClient != nil {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()

	addr, err := m.findModuleByCapability(ctx, "downloader")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial downloader: %w", err)
	}
	m.mu.Lock()
	m.downloaderConn = conn
	m.downloaderClient = downloaderv1.NewTorrentServiceClient(conn)
	m.mu.Unlock()
	return nil
}

func (m *Module) ensureFormats(ctx context.Context) error {
	m.mu.RLock()
	if m.formatsClient != nil {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()

	addr, err := m.findModuleByCapability(ctx, "media.scoring")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial formats: %w", err)
	}
	m.mu.Lock()
	m.formatsConn = conn
	m.formatsClient = formatsv1.NewFormatServiceClient(conn)
	m.mu.Unlock()
	return nil
}

func (m *Module) ensureMovies(ctx context.Context) error {
	m.mu.RLock()
	if m.moviesClient != nil {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()

	addr, err := m.findModuleByCapability(ctx, "media.library.movies")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial movies: %w", err)
	}
	m.mu.Lock()
	m.moviesConn = conn
	m.moviesClient = mgmntv1.NewMovieManagementServiceClient(conn)
	m.mu.Unlock()
	return nil
}

func (m *Module) ensureTV(ctx context.Context) error {
	m.mu.RLock()
	if m.tvClient != nil {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()

	addr, err := m.findModuleByCapability(ctx, "media.library.tv")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial tvshows: %w", err)
	}
	m.mu.Lock()
	m.tvConn = conn
	m.tvClient = tvmgmtv1.NewTvManagementServiceClient(conn)
	m.mu.Unlock()
	return nil
}

// ── Event Subscriptions ─────────────────────────────────────────

func (m *Module) subscribeToMediaEvents() {
	time.Sleep(15 * time.Second)
	if m.mc == nil {
		slog.Warn("media-automation: not connected to core, skipping event subscriptions")
		return
	}

	eventTypes := []string{
		contracts.EventMovieAdded,
		contracts.EventTVAdded,
		contracts.EventMovieRemoved,
		contracts.EventTVRemoved,
		contracts.EventMovieFileAdded,
		contracts.EventTVEpisodeFileAdded,
		contracts.EventFileImported,
	}

	for _, et := range eventTypes {
		ch, cancel, err := m.mc.Events.Subscribe(context.Background(), et)
		if err != nil {
			slog.Warn("subscribe to event", "type", et, "error", err)
			continue
		}
		go m.handleEventStream(et, ch, cancel)
		slog.Info("subscribed to events", "type", et)
	}
}

func (m *Module) handleEventStream(eventType string, ch <-chan *eventsv1.Event, cancel context.CancelFunc) {
	for evt := range ch {
		switch eventType {
		case contracts.EventMovieAdded:
			var payload contracts.MovieAddedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.MovieID != "" {
				m.upsertWanted(context.Background(), wantedEntry{
					ItemType: "movie", ItemID: payload.MovieID,
					TmdbID: payload.TMDBID, Title: payload.Title,
				})
			}
		case contracts.EventTVAdded:
			var payload contracts.TVAddedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.SeriesID != "" {
				go m.syncTVSeriesWithRetry(payload.SeriesID)
			}
		case contracts.EventMovieRemoved:
			var payload contracts.MovieRemovedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.MovieID != "" {
				m.removeWanted(context.Background(), "movie", payload.MovieID)
			}
		case contracts.EventTVRemoved:
			var payload contracts.TVRemovedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.SeriesID != "" {
				m.syncWantedFromLibraries(context.Background())
			}
		case contracts.EventMovieFileAdded:
			var payload contracts.MovieFileAddedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.MovieID != "" {
				m.removeWanted(context.Background(), "movie", payload.MovieID)
			}
		case contracts.EventTVEpisodeFileAdded:
			var payload contracts.TVEpisodeFileAddedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.EpisodeID != "" {
				m.removeWanted(context.Background(), "tv", payload.EpisodeID)
			}
		case contracts.EventFileImported:
			var payload contracts.FileImportedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.OriginalPath != "" {
				slog.Info("file imported event received", "title", payload.Title, "path", payload.DestinationPath)
			}
		}
	}
	cancel()
}

func (m *Module) syncTVSeriesWithRetry(seriesID string) {
	for i := 0; i < 5; i++ {
		time.Sleep(2 * time.Second)
		n, err := m.syncWantedTV(context.Background(), seriesID, nil)
		if err != nil {
			slog.Debug("tv series wanted sync", "series", seriesID, "attempt", i+1, "error", err)
			continue
		}
		if n > 0 || i == 4 {
			return
		}
	}
}

// ── Library sync (wanted cache) ────────────────────────────────

type wantedEntry struct {
	ItemType         string
	ItemID           string
	TmdbID           int32
	Title            string
	Year             int32
	SeasonNumber     int32
	EpisodeNumber    int32
	AbsoluteNumber   int32
	SeriesType       string
	SeriesID         string
	QualityProfileID string
}

func (m *Module) syncWantedFromLibraries(ctx context.Context) {
	seen := make(map[string]struct{})
	moviesOK, tvOK := false, false
	if _, err := m.syncWantedMovies(ctx, seen); err != nil {
		slog.Debug("sync missing movies", "error", err)
	} else {
		moviesOK = true
	}
	if _, err := m.syncWantedTV(ctx, "", seen); err != nil {
		slog.Debug("sync missing episodes", "error", err)
	} else {
		tvOK = true
	}
	m.pruneWantedNotInLibraries(ctx, seen, moviesOK, tvOK)
}

func (m *Module) syncWantedMovies(ctx context.Context, seen map[string]struct{}) (int, error) {
	if err := m.ensureMovies(ctx); err != nil {
		return 0, err
	}
	m.mu.RLock()
	client := m.moviesClient
	m.mu.RUnlock()

	page := int32(1)
	totalUpserted := 0
	for {
		resp, err := client.ListMissing(ctx, &mgmntv1.ListMissingRequest{Page: page, PageSize: 100})
		if err != nil {
			return totalUpserted, err
		}
		for _, item := range resp.GetItems() {
			if seen != nil {
				seen["movie:"+item.GetMovieId()] = struct{}{}
			}
			m.upsertWanted(ctx, wantedEntry{
				ItemType: "movie", ItemID: item.GetMovieId(),
				TmdbID: item.GetTmdbId(), Title: item.GetTitle(), Year: item.GetYear(),
				QualityProfileID: item.GetQualityProfileId(),
			})
			totalUpserted++
		}
		if int(page)*int(resp.GetPageSize()) >= int(resp.GetTotal()) || len(resp.GetItems()) == 0 {
			break
		}
		page++
	}
	return totalUpserted, nil
}

func (m *Module) syncWantedTV(ctx context.Context, seriesID string, seen map[string]struct{}) (int, error) {
	if err := m.ensureTV(ctx); err != nil {
		return 0, err
	}
	m.mu.RLock()
	client := m.tvClient
	m.mu.RUnlock()

	type seasonKey struct {
		seriesID string
		season   int32
	}
	type seasonBucket struct {
		items []*tvmgmtv1.MissingEpisodeItem
	}
	buckets := map[seasonKey]*seasonBucket{}

	page := int32(1)
	for {
		resp, err := client.ListMissing(ctx, &tvmgmtv1.ListMissingRequest{
			Page: page, PageSize: 100, SeriesId: seriesID,
		})
		if err != nil {
			return 0, err
		}
		for _, item := range resp.GetItems() {
			if seen != nil {
				seen["tv:"+item.GetEpisodeId()] = struct{}{}
			}
			k := seasonKey{seriesID: item.GetSeriesId(), season: item.GetSeasonNumber()}
			b := buckets[k]
			if b == nil {
				b = &seasonBucket{}
				buckets[k] = b
			}
			b.items = append(b.items, item)
		}
		if int(page)*int(resp.GetPageSize()) >= int(resp.GetTotal()) || len(resp.GetItems()) == 0 {
			break
		}
		page++
	}

	totalUpserted := 0
	for k, b := range buckets {
		if len(b.items) == 0 {
			continue
		}
		sample := b.items[0]
		if seasonPackEligible(len(b.items)) {
			packID := fmt.Sprintf("%s:S%d:pack", k.seriesID, k.season)
			if seen != nil {
				seen["tv:"+packID] = struct{}{}
			}
			m.upsertWanted(ctx, wantedEntry{
				ItemType: "tv", ItemID: packID,
				TmdbID: sample.GetTmdbId(), Title: sample.GetTitle(), Year: sample.GetYear(),
				SeasonNumber: k.season, EpisodeNumber: 0,
				SeriesType: sample.GetSeriesType(), SeriesID: k.seriesID,
				QualityProfileID: sample.GetQualityProfileId(),
			})
			totalUpserted++
			continue
		}
		for _, item := range b.items {
			m.upsertWanted(ctx, wantedEntry{
				ItemType: "tv", ItemID: item.GetEpisodeId(),
				TmdbID: item.GetTmdbId(), Title: item.GetTitle(), Year: item.GetYear(),
				SeasonNumber: item.GetSeasonNumber(), EpisodeNumber: item.GetEpisodeNumber(),
				AbsoluteNumber: item.GetAbsoluteNumber(), SeriesType: item.GetSeriesType(),
				SeriesID: item.GetSeriesId(), QualityProfileID: item.GetQualityProfileId(),
			})
			totalUpserted++
		}
	}
	return totalUpserted, nil
}

func seasonPackEligible(missingCount int) bool {
	return missingCount >= 3
}

func (m *Module) pruneWantedNotInLibraries(ctx context.Context, seen map[string]struct{}, moviesOK, tvOK bool) {
	if !moviesOK && !tvOK {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return
	}
	rows, err := m.db.QueryContext(ctx, `SELECT item_type, item_id FROM wanted_items WHERE missing = 1`)
	if err != nil {
		return
	}
	defer rows.Close()
	var toDelete [][2]string
	for rows.Next() {
		var itemType, itemID string
		if err := rows.Scan(&itemType, &itemID); err != nil {
			continue
		}
		if itemType == "movie" && !moviesOK {
			continue
		}
		if itemType == "tv" && !tvOK {
			continue
		}
		if _, ok := seen[itemType+":"+itemID]; !ok {
			toDelete = append(toDelete, [2]string{itemType, itemID})
		}
	}
	for _, d := range toDelete {
		m.db.ExecContext(ctx, `DELETE FROM wanted_items WHERE item_type = ? AND item_id = ?`, d[0], d[1])
	}
}

func (m *Module) upsertWanted(ctx context.Context, e wantedEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil || e.ItemID == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("w_%s_%s", e.ItemType, e.ItemID)
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO wanted_items (id, item_type, item_id, tmdb_id, title, year, season_number, episode_number, monitored, missing, quality_profile_id, absolute_number, series_type, series_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(item_type, item_id) DO UPDATE SET
		   tmdb_id = excluded.tmdb_id,
		   title = excluded.title,
		   year = excluded.year,
		   season_number = excluded.season_number,
		   episode_number = excluded.episode_number,
		   monitored = 1,
		   missing = 1,
		   quality_profile_id = excluded.quality_profile_id,
		   absolute_number = excluded.absolute_number,
		   series_type = excluded.series_type,
		   series_id = excluded.series_id,
		   updated_at = excluded.updated_at`,
		id, e.ItemType, e.ItemID, e.TmdbID, e.Title, e.Year,
		e.SeasonNumber, e.EpisodeNumber, e.QualityProfileID,
		e.AbsoluteNumber, e.SeriesType, e.SeriesID, now, now,
	)
	if err != nil {
		slog.Debug("upsert wanted", "error", err, "item", e.ItemID)
	}
}

func (m *Module) removeWanted(ctx context.Context, itemType, itemID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil || itemID == "" {
		return
	}
	m.db.ExecContext(ctx, `DELETE FROM wanted_items WHERE item_type = ? AND item_id = ?`, itemType, itemID)
}

// ── RSS Sync Loop ──────────────────────────────────────────────

func (m *Module) rssSyncLoop() {
	const interval = 15 * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	time.Sleep(10 * time.Second)
	m.rssSync()

	for range ticker.C {
		m.rssSync()
	}
}

func (m *Module) rssSync() {
	slog.Debug("rss sync cycle starting")
	m.syncWantedFromLibraries(context.Background())
	m.searchQueuedItems()
}

func (m *Module) searchQueuedItems() {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return
	}

	rows, err := db.Query(`SELECT id, item_type, tmdb_id, title, year, season_number, episode_number, quality_profile_id, absolute_number, series_type FROM wanted_items WHERE monitored = 1 AND missing = 1`)
	if err != nil {
		slog.Error("query wanted items", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, itemType, title, profileID, seriesType string
		var tmdbID, year, seasonNum, epNum, absNum int64
		if err := rows.Scan(&id, &itemType, &tmdbID, &title, &year, &seasonNum, &epNum, &profileID, &absNum, &seriesType); err != nil {
			slog.Error("scan wanted row", "error", err)
			continue
		}
		m.searchAndStore(context.Background(), id, itemType, title, int(tmdbID), int(year), int(seasonNum), int(epNum), int(absNum), seriesType, profileID)
	}
}

func (m *Module) searchAndStore(ctx context.Context, itemID, itemType, title string, tmdbID, year, season, episode, absolute int, seriesType, profileID string) {
	results := m.searchWithIndexer(ctx, itemType, title, year, season, episode, absolute, seriesType, 50, profileID)
	if len(results) > 0 {
		best := results[0]
		slog.Info("found releases for "+title, "item", itemID, "matches", len(results), "best", best.Title, "score", best.Score)

		if best.DownloadURL != "" {
			_, err := m.Dispatch(ctx, &automationv1.DispatchRequest{
				Guid:             best.GUID,
				Title:            best.Title,
				DownloadUrl:      best.DownloadURL,
				DownloadProtocol: best.DownloadProtocol,
				Size:             best.Size,
				Score:            int32(best.Score),
				IndexerName:      best.IndexerName,
				ItemType:         itemType,
				ItemId:           itemID,
				TmdbId:           int32(tmdbID),
			})
			if err != nil {
				slog.Warn("dispatch failed for "+title, "item", itemID, "error", err)
			}
		}
	}

	m.mu.Lock()
	m.db.Exec(`UPDATE wanted_items SET last_searched = datetime('now'), updated_at = datetime('now') WHERE id = ?`, itemID)
	m.mu.Unlock()
}

// ── Quality Scoring ────────────────────────────────────────────

var (
	reResolution = regexp.MustCompile(`(?i)(\d{3,4})[pi]`)
	reRemux      = regexp.MustCompile(`(?i)remux`)
	reBluRay     = regexp.MustCompile(`(?i)bluray|brrip|bdrip|blu-ray`)
	reWebDL      = regexp.MustCompile(`(?i)web[-\s]?dl|webdl|webrip`)
	reHDTV       = regexp.MustCompile(`(?i)hdtv`)
	reCam        = regexp.MustCompile(`(?i)cam|ts|tc|hdts|hd-cam`)
	reRawHD      = regexp.MustCompile(`(?i)rawhd|raw.?hd`)
)

type scoredRelease struct {
	GUID             string
	Title            string
	Size             int64
	Seeders          int32
	Peers            int32
	IndexerName      string
	DownloadURL      string
	InfoURL          string
	DownloadProtocol string
	Category         string
	SubCategory      string
	Score            int
}

func (m *Module) scoreWithFormatsFallback(ctx context.Context, results []*indexerv1.SearchResult, title, profileID string) []scoredRelease {
	var scored []scoredRelease
	if err := m.ensureFormats(ctx); err == nil {
		m.mu.RLock()
		fc := m.formatsClient
		m.mu.RUnlock()
		minScore := m.profileMinScore(ctx, profileID)
		for _, r := range results {
			resp, err := fc.ScoreRelease(ctx, &formatsv1.ScoreReleaseRequest{
				Title:     r.GetTitle(),
				Size:      r.GetSize(),
				Seeders:   r.GetSeeders(),
				ProfileId: profileID,
			})
			if err != nil {
				continue
			}
			s := int(resp.GetTotalScore())
			if s > 0 && (minScore <= 0 || s >= minScore) {
				scored = append(scored, scoredRelease{
					GUID:             r.GetGuid(),
					Title:            r.GetTitle(),
					Size:             r.GetSize(),
					Seeders:          r.GetSeeders(),
					Peers:            r.GetPeers(),
					IndexerName:      r.GetIndexerName(),
					DownloadURL:      r.GetDownloadUrl(),
					InfoURL:          r.GetInfoUrl(),
					DownloadProtocol: r.GetDownloadProtocol(),
					Category:         r.GetCategory(),
					SubCategory:      r.GetSubCategory(),
					Score:            s,
				})
			}
		}
	}
	if len(scored) == 0 {
		return scoreReleases(results, title)
	}
	sortScoredDesc(scored)
	if len(scored) > 100 {
		scored = scored[:100]
	}
	return scored
}

func (m *Module) profileMinScore(ctx context.Context, profileID string) int {
	if err := m.ensureFormats(ctx); err != nil {
		return 0
	}
	m.mu.RLock()
	fc := m.formatsClient
	m.mu.RUnlock()
	resp, err := fc.ListProfiles(ctx, &formatsv1.ListProfilesRequest{})
	if err != nil {
		return 0
	}
	profiles := resp.GetProfiles()
	if profileID != "" {
		for _, p := range profiles {
			if p.GetId() == profileID {
				return int(p.GetMinScore())
			}
		}
		return 0
	}
	if len(profiles) > 0 {
		return int(profiles[0].GetMinScore())
	}
	return 0
}

func filterByMinScore(scored []scoredRelease, minScore int) []scoredRelease {
	if minScore <= 0 {
		return scored
	}
	out := make([]scoredRelease, 0, len(scored))
	for _, s := range scored {
		if s.Score >= minScore {
			out = append(out, s)
		}
	}
	return out
}

func scoreReleases(results []*indexerv1.SearchResult, title string) []scoredRelease {
	var scored []scoredRelease
	for _, r := range results {
		s := scoreRelease(r, title)
		if s > 0 {
			scored = append(scored, scoredRelease{
				GUID:             r.GetGuid(),
				Title:            r.GetTitle(),
				Size:             r.GetSize(),
				Seeders:          r.GetSeeders(),
				Peers:            r.GetPeers(),
				IndexerName:      r.GetIndexerName(),
				DownloadURL:      r.GetDownloadUrl(),
				InfoURL:          r.GetInfoUrl(),
				DownloadProtocol: r.GetDownloadProtocol(),
				Category:         r.GetCategory(),
				SubCategory:      r.GetSubCategory(),
				Score:            s,
			})
		}
	}
	sortScoredDesc(scored)
	if len(scored) > 100 {
		scored = scored[:100]
	}
	return scored
}

func scoreRelease(r *indexerv1.SearchResult, title string) int {
	score := 0
	name := r.GetTitle()

	resolution := parseResolution(name)
	switch {
	case resolution >= 2160:
		score += 120
	case resolution >= 1080:
		score += 100
	case resolution >= 720:
		score += 80
	case resolution >= 576:
		score += 60
	default:
		score += 40
	}

	switch {
	case reRemux.MatchString(name):
		score += 40
	case reBluRay.MatchString(name):
		score += 30
	case reRawHD.MatchString(name):
		score += 25
	case reWebDL.MatchString(name):
		score += 20
	case reHDTV.MatchString(name):
		score += 10
	case reCam.MatchString(name):
		score -= 100
	}

	if r.GetSeeders() > 100 {
		score += 10
	} else if r.GetSeeders() > 30 {
		score += 5
	} else if r.GetSeeders() > 5 {
		score += 2
	} else if r.GetSeeders() == 0 && r.GetDownloadProtocol() == "http" {
		score -= 5
	}

	if r.GetSize() > 0 && resolution > 0 {
		minSize := int64(math.Max(float64(resolution), 1)) * 50 * 1024 * 1024
		maxSize := int64(math.Max(float64(resolution), 1)) * 1000 * 1024 * 1024
		if r.GetSize() < minSize {
			score -= 20
		} else if r.GetSize() > maxSize {
			score -= 10
		}
	}

	return score
}

func parseResolution(name string) int {
	m := reResolution.FindStringSubmatch(name)
	if len(m) > 1 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			return v
		}
	}
	lower := strings.ToLower(name)
	if strings.Contains(lower, "4k") || strings.Contains(lower, "uhd") {
		return 2160
	}
	if strings.Contains(lower, "1080") || strings.Contains(lower, "hd") {
		return 1080
	}
	return 0
}

func sortScoredDesc(scored []scoredRelease) {
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].Score > scored[i].Score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}
}

// ── gRPC API ───────────────────────────────────────────────────

func (m *Module) SearchItem(ctx context.Context, req *automationv1.SearchItemRequest) (*automationv1.SearchItemResponse, error) {
	if req.GetQuery() == "" {
		return nil, fmt.Errorf("query is required")
	}

	results := m.searchWithIndexer(ctx, req.GetItemType(), req.GetQuery(),
		int(req.GetYear()), int(req.GetSeason()), int(req.GetEpisode()), 0, "", int(req.GetLimit()), req.GetQualityProfileId())

	var matches []*automationv1.ReleaseMatch
	for _, r := range results {
		matches = append(matches, &automationv1.ReleaseMatch{
			Guid:             r.GUID,
			Title:            r.Title,
			Size:             r.Size,
			Seeders:          r.Seeders,
			Peers:            r.Peers,
			IndexerName:      r.IndexerName,
			DownloadUrl:      r.DownloadURL,
			InfoUrl:          r.InfoURL,
			DownloadProtocol: r.DownloadProtocol,
			Score:            int32(r.Score),
			Category:         r.Category,
			SubCategory:      r.SubCategory,
		})
	}
	return &automationv1.SearchItemResponse{Matches: matches}, nil
}

func (m *Module) searchWithIndexer(ctx context.Context, itemType, query string, year, season, episode, absolute int, seriesType string, limit int, profileID string) []scoredRelease {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := m.ensureIndexer(ctx); err != nil {
		slog.Debug("no indexer available", "error", err)
		return nil
	}

	m.mu.RLock()
	client := m.indexerClient
	m.mu.RUnlock()

	searchType := "search"
	switch itemType {
	case "movie":
		searchType = "movie"
	case "tv":
		searchType = "tv"
	}

	maxLimit := int32(limit)
	if maxLimit <= 0 || maxLimit > 100 {
		maxLimit = 50
	}

	searchQuery := query
	wantPack := itemType == "tv" && season > 0 && episode == 0
	if wantPack {
		searchQuery = fmt.Sprintf("%s S%02d", query, season)
	} else if seriesType == "anime" && absolute > 0 {
		searchQuery = fmt.Sprintf("%s %d", query, absolute)
	} else if itemType == "tv" && season > 0 && episode > 0 {
		searchQuery = fmt.Sprintf("%s S%02dE%02d", query, season, episode)
	}

	resp, err := client.Search(ctx, &indexerv1.SearchRequest{
		Query:    searchQuery,
		Type:     searchType,
		Year:     int32(year),
		Season:   int32(season),
		Episode:  int32(episode),
		Absolute: int32(absolute),
		Limit:    maxLimit,
	})
	if err != nil {
		slog.Debug("indexer search failed", "error", err)
		return nil
	}

	scored := m.scoreWithFormatsFallback(ctx, resp.GetResults(), query, profileID)
	if wantPack || (seriesType == "anime" && absolute > 0) {
		var filtered []scoredRelease
		for i := range scored {
			scored[i].Score += scoreTVReleaseBoost(scored[i].Title, season, episode, absolute, wantPack, seriesType)
			if scored[i].Score > 0 {
				filtered = append(filtered, scored[i])
			}
		}
		scored = filtered
		sortScoredDesc(scored)
	}
	return scored
}

var (
	reSeasonPackTitle = regexp.MustCompile(`(?i)(?:season[.\s_-]*pack|complete[.\s_-]*season|season[.\s_-]*\d{1,2}[.\s_-]*complete|S\d{1,2}[.\s_-]*(?:complete|pack))`)
	reSeasonInTitle   = regexp.MustCompile(`(?i)(?:^|[^0-9])S(\d{1,2})(?:[^0-9E]|$)`)
)

func scoreTVReleaseBoost(name string, season, episode, absolute int, wantPack bool, seriesType string) int {
	boost := 0
	if wantPack {
		if reSeasonPackTitle.MatchString(name) {
			boost += 50
		}
		if m := reSeasonInTitle.FindStringSubmatch(name); len(m) >= 2 {
			if sn, err := strconv.Atoi(m[1]); err == nil && sn != season {
				return -200
			}
		}
		if episodeToken := fmt.Sprintf("E%02d", 1); strings.Contains(strings.ToUpper(name), episodeToken) && !reSeasonPackTitle.MatchString(name) {
			boost -= 20
		}
	}
	if seriesType == "anime" && absolute > 0 {
		absStr := strconv.Itoa(absolute)
		if strings.Contains(name, absStr) || strings.Contains(name, fmt.Sprintf("[%d]", absolute)) || strings.Contains(strings.ToUpper(name), fmt.Sprintf("EP%03d", absolute)) {
			boost += 40
		}
	}
	return boost
}

func (m *Module) Dispatch(ctx context.Context, req *automationv1.DispatchRequest) (*automationv1.DispatchResponse, error) {
	if err := m.ensureDownloader(ctx); err != nil {
		return nil, fmt.Errorf("no downloader available: %w", err)
	}

	m.mu.RLock()
	client := m.downloaderClient
	m.mu.RUnlock()

	addResp, err := client.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri:   req.GetDownloadUrl(),
		Label: req.GetItemType(),
	})
	if err != nil {
		return nil, fmt.Errorf("add torrent: %w", err)
	}

	m.mu.Lock()
	now := time.Now().UTC().Format(time.RFC3339)
	m.db.ExecContext(ctx,
		`INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'sent', ?, ?)`,
		fmt.Sprintf("dl_%s_%d", req.GetGuid(), time.Now().UnixNano()),
		req.GetItemId(), req.GetGuid(), req.GetTitle(), req.GetIndexerName(),
		req.GetSize(), req.GetScore(), req.GetDownloadUrl(), req.GetDownloadProtocol(),
		now, now,
	)
	m.mu.Unlock()

	slog.Info("dispatched download", "title", req.GetTitle(), "protocol", req.GetDownloadProtocol(), "id", addResp.GetId())
	return &automationv1.DispatchResponse{
		DownloadId: addResp.GetId(),
		Status:     "sent",
	}, nil
}

func (m *Module) AddToQueue(ctx context.Context, req *automationv1.AddToQueueRequest) (*automationv1.AddToQueueResponse, error) {
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	m.upsertWanted(ctx, wantedEntry{
		ItemType:         req.GetItemType(),
		ItemID:           req.GetItemId(),
		TmdbID:           req.GetTmdbId(),
		Title:            req.GetTitle(),
		Year:             req.GetYear(),
		SeasonNumber:     req.GetSeasonNumber(),
		EpisodeNumber:    req.GetEpisodeNumber(),
		QualityProfileID: req.GetQualityProfileId(),
	})
	id := fmt.Sprintf("w_%s_%s", req.GetItemType(), req.GetItemId())
	return &automationv1.AddToQueueResponse{QueueId: id}, nil
}

func (m *Module) GetQueue(ctx context.Context, req *automationv1.GetQueueRequest) (*automationv1.GetQueueResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	query := `SELECT id, item_type, item_id, tmdb_id, title, year, season_number, episode_number, monitored, missing, last_searched, created_at, updated_at, quality_profile_id FROM wanted_items`
	countQuery := `SELECT COUNT(*) FROM wanted_items`
	var args []any
	var where []string

	if req.GetFilter() != "" {
		where = append(where, `item_type = ?`)
		args = append(args, req.GetFilter())
	}
	if len(where) > 0 {
		clause := ` WHERE ` + strings.Join(where, ` AND `)
		query += clause
		countQuery += clause
	}

	var total int
	m.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	query += ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	qargs := append(args, pageSize, offset)

	rows, err := m.db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return nil, fmt.Errorf("query queue: %w", err)
	}
	defer rows.Close()

	var items []*automationv1.QueueItem
	for rows.Next() {
		var id, itemType, itemID, title, createdAt, updatedAt, profileID string
		var tmdbID, year, seasonNum, epNum int64
		var monitored, missing int
		var lastSearched sql.NullString
		if err := rows.Scan(&id, &itemType, &itemID, &tmdbID, &title, &year, &seasonNum, &epNum, &monitored, &missing, &lastSearched, &createdAt, &updatedAt, &profileID); err != nil {
			slog.Error("scan queue row", "error", err)
			continue
		}
		items = append(items, &automationv1.QueueItem{
			Id: id, ItemType: itemType, ItemId: itemID,
			TmdbId: int32(tmdbID), Title: title, Year: int32(year),
			SeasonNumber: int32(seasonNum), EpisodeNumber: int32(epNum),
			Monitored: monitored != 0, Missing: missing != 0,
			LastSearched: lastSearched.String,
			CreatedAt:    createdAt, UpdatedAt: updatedAt,
			QualityProfileId: profileID,
		})
	}

	return &automationv1.GetQueueResponse{
		Items:    items,
		Total:    int32(total),
		Page:     int32(page),
		PageSize: int32(pageSize),
	}, nil
}

func (m *Module) GetHistory(ctx context.Context, req *automationv1.GetHistoryRequest) (*automationv1.GetHistoryResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var total int
	m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_history`).Scan(&total)

	rows, err := m.db.QueryContext(ctx,
		`SELECT id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, completed_at, created_at FROM download_history ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		pageSize, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	var records []*automationv1.DownloadRecord
	for rows.Next() {
		var id, wantedItemID, guid, title, indexer, downloadURL, downloadProto, status, createdAt string
		var size, score int64
		var sentAt, completedAt sql.NullString
		if err := rows.Scan(&id, &wantedItemID, &guid, &title, &indexer, &size, &score, &downloadURL, &downloadProto, &status, &sentAt, &completedAt, &createdAt); err != nil {
			slog.Error("scan history row", "error", err)
			continue
		}
		records = append(records, &automationv1.DownloadRecord{
			Id: id, WantedItemId: wantedItemID,
			Guid: guid, Title: title, Indexer: indexer,
			Size: size, Score: int32(score),
			DownloadUrl: downloadURL, DownloadProtocol: downloadProto,
			Status: status, SentAt: sentAt.String,
			CompletedAt: completedAt.String, CreatedAt: createdAt,
		})
	}

	return &automationv1.GetHistoryResponse{
		Records:  records,
		Total:    int32(total),
		Page:     int32(page),
		PageSize: int32(pageSize),
	}, nil
}

func fileDir(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return "."
	}
	return path[:idx]
}

var _ contracts.Module = (*Module)(nil)
