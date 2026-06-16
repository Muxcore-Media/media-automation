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

	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
	indexerv1 "github.com/Muxcore-Media/indexer-prowlarr/proto/indexerv1"
	formatsv1 "github.com/Muxcore-Media/media-custom-formats/proto/formatsv1"

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
			last_searched  TEXT,
			created_at     TEXT NOT NULL,
			updated_at     TEXT NOT NULL,
			UNIQUE(item_type, item_id)
		)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create wanted_items table: %w", err)
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
				m.AddToQueue(context.Background(), &automationv1.AddToQueueRequest{
					ItemType: "movie",
					ItemId:   payload.MovieID,
					TmdbId:   payload.TMDBID,
					Title:    payload.Title,
				})
			}
		case contracts.EventTVAdded:
			var payload contracts.TVAddedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.SeriesID != "" {
				m.AddToQueue(context.Background(), &automationv1.AddToQueueRequest{
					ItemType: "tv",
					ItemId:   payload.SeriesID,
					TmdbId:   payload.TMDBID,
					Title:    payload.Name,
				})
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
	m.searchQueuedItems()
}

func (m *Module) searchQueuedItems() {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return
	}

	rows, err := db.Query(`SELECT id, item_type, tmdb_id, title, year, season_number, episode_number FROM wanted_items WHERE monitored = 1 AND missing = 1`)
	if err != nil {
		slog.Error("query wanted items", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, itemType, title string
		var tmdbID, year, seasonNum, epNum int64
		if err := rows.Scan(&id, &itemType, &tmdbID, &title, &year, &seasonNum, &epNum); err != nil {
			slog.Error("scan wanted row", "error", err)
			continue
		}
		m.searchAndStore(context.Background(), id, itemType, title, int(tmdbID), int(year), int(seasonNum), int(epNum))
	}
}

func (m *Module) searchAndStore(ctx context.Context, itemID, itemType, title string, tmdbID, year, season, episode int) {
	results := m.searchWithIndexer(ctx, itemType, title, year, season, episode, 50)
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
			} else {
				m.mu.Lock()
				m.db.Exec(`UPDATE wanted_items SET missing = 0, updated_at = datetime('now') WHERE id = ?`, itemID)
				m.mu.Unlock()
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

func (m *Module) scoreWithFormatsFallback(ctx context.Context, results []*indexerv1.IndexerResult, title string) []scoredRelease {
	var scored []scoredRelease
	if err := m.ensureFormats(ctx); err == nil {
		m.mu.RLock()
		fc := m.formatsClient
		m.mu.RUnlock()
		for _, r := range results {
			resp, err := fc.ScoreRelease(ctx, &formatsv1.ScoreReleaseRequest{
				Title:   r.GetTitle(),
				Size:    r.GetSize(),
				Seeders: r.GetSeeders(),
			})
			if err != nil {
				continue
			}
			s := int(resp.GetTotalScore())
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

func scoreReleases(results []*indexerv1.IndexerResult, title string) []scoredRelease {
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

func scoreRelease(r *indexerv1.IndexerResult, title string) int {
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
		int(req.GetYear()), int(req.GetSeason()), int(req.GetEpisode()), int(req.GetLimit()))

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

func (m *Module) searchWithIndexer(ctx context.Context, itemType, query string, year, season, episode, limit int) []scoredRelease {
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

	resp, err := client.Search(ctx, &indexerv1.SearchRequest{
		Query:   query,
		Type:    searchType,
		Year:    int32(year),
		Season:  int32(season),
		Episode: int32(episode),
		Limit:   maxLimit,
	})
	if err != nil {
		slog.Debug("indexer search failed", "error", err)
		return nil
	}

	scored := m.scoreWithFormatsFallback(ctx, resp.GetResults(), query)
	return scored
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("w_%s_%d", req.GetItemType(), time.Now().UnixNano())

	_, err := m.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO wanted_items (id, item_type, item_id, tmdb_id, title, year, season_number, episode_number, monitored, missing, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)`,
		id, req.GetItemType(), req.GetItemId(), req.GetTmdbId(), req.GetTitle(),
		req.GetYear(), req.GetSeasonNumber(), req.GetEpisodeNumber(), now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert wanted item: %w", err)
	}

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

	query := `SELECT id, item_type, item_id, tmdb_id, title, year, season_number, episode_number, monitored, missing, last_searched, created_at, updated_at FROM wanted_items`
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
		var id, itemType, itemID, title, createdAt, updatedAt string
		var tmdbID, year, seasonNum, epNum int64
		var monitored, missing int
		var lastSearched sql.NullString
		if err := rows.Scan(&id, &itemType, &itemID, &tmdbID, &title, &year, &seasonNum, &epNum, &monitored, &missing, &lastSearched, &createdAt, &updatedAt); err != nil {
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
