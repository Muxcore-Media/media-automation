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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	automationv1 "github.com/Muxcore-Media/media-automation/proto/automationv1"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	indexerv1 "github.com/Muxcore-Media/contracts-indexer/muxcore/indexer/v1"
	formatsv1 "github.com/Muxcore-Media/media-custom-formats/proto/formatsv1"
	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	scannerv1 "github.com/Muxcore-Media/media-scanner/proto/scannerv1"
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

	enableAutomaticSearch   bool
	enableAutomaticUpgrades bool
	rssSyncMinutes          int

	indexerConns   map[string]*grpc.ClientConn
	indexerClients map[string]indexerv1.IndexerServiceClient
	// testIndexerClients, when set, skips discovery and is used for Search fan-out (tests).
	testIndexerClients map[string]indexerv1.IndexerServiceClient

	downloaderConn   *grpc.ClientConn
	downloaderClient cdlv1.DownloaderServiceClient
	formatsConn      *grpc.ClientConn
	formatsClient    formatsv1.FormatServiceClient
	moviesConn       *grpc.ClientConn
	moviesClient     mgmntv1.MovieManagementServiceClient
	tvConn           *grpc.ClientConn
	tvClient         tvmgmtv1.TvManagementServiceClient
	scannerConn      *grpc.ClientConn
	scannerClient    scannerv1.ScannerServiceClient
	// testScannerClient, when set, skips discovery and is used for ImportPath (tests).
	testScannerClient scannerv1.ScannerServiceClient
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
		id:                      cfg.ID,
		dbPath:                  cfg.DBPath,
		grpcAddr:                cfg.GRPCAddr,
		enableAutomaticSearch:   true,
		enableAutomaticUpgrades: true,
		rssSyncMinutes:          15,
		indexerConns:            make(map[string]*grpc.ClientConn),
		indexerClients:          make(map[string]indexerv1.IndexerServiceClient),
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:             m.id,
		Name:           "Media Automation",
		Version:      "0.1.7",
		Roles:          []string{"automation"},
		Description:    "Automation engine — searches searchers, scores releases, and dispatches downloads for wanted media",
		Author:         "MuxCore",
		Capabilities:   []string{"media.automation", "settings"},
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
		`ALTER TABLE wanted_items ADD COLUMN clean_titles TEXT DEFAULT '[]'`,
		`ALTER TABLE wanted_items ADD COLUMN current_score INTEGER DEFAULT 0`,
		`ALTER TABLE wanted_items ADD COLUMN file_acquired_at TEXT DEFAULT ''`,
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
			created_at      TEXT NOT NULL,
			download_id     TEXT DEFAULT ''
		)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create download_history table: %w", err)
	}
	if err := m.migrateDelayTables(ctx, db); err != nil {
		db.Close()
		return fmt.Errorf("migrate delay tables: %w", err)
	}
	if err := m.migrateSeriesOverrides(ctx, db); err != nil {
		db.Close()
		return fmt.Errorf("migrate series overrides: %w", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE download_history ADD COLUMN download_id TEXT DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return fmt.Errorf("migrate download_history: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_download_history_download_id ON download_history(download_id)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create download_history download_id index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_wanted_type ON wanted_items(item_type, missing)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create wanted index: %w", err)
	}
	if err := m.migrateDelayTables(ctx, db); err != nil {
		db.Close()
		return fmt.Errorf("migrate delay tables: %w", err)
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
	m.registerSettingsMesh(m.grpcSrv)

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
	m.mu.Lock()
	for id, conn := range m.indexerConns {
		conn.Close()
		delete(m.indexerConns, id)
		delete(m.indexerClients, id)
	}
	m.mu.Unlock()
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
	if m.scannerConn != nil {
		m.scannerConn.Close()
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
	insecureMode := os.Getenv("MUXCORE_INSECURE_DISABLE_TLS") == "true" || os.Getenv("MUXCORE_GRPC_INSECURE") == "true"

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
			return dialAddrForModule(mod.GetId(), mod.GetHttpAddr()), nil
		}
	}
	return "", fmt.Errorf("no %s module found", cap)
}

// dialAddrForModule maps discovery HttpAddr to a dial target.
// Explicit hosts (e.g. 127.0.0.1) are preserved for host-process MVP.
// Empty / wildcard hosts rewrite to module ID for Docker DNS, unless
// MUXCORE_MESH_DIAL_LOCAL=true (then 127.0.0.1).
func dialAddrForModule(moduleID, httpAddr string) string {
	if httpAddr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil || port == "" {
		return httpAddr
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		return net.JoinHostPort(host, port)
	}
	if os.Getenv("MUXCORE_MESH_DIAL_LOCAL") == "true" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	if moduleID != "" {
		return net.JoinHostPort(moduleID, port)
	}
	return httpAddr
}

// ── Module connections ─────────────────────────────────────────

// syncIndexers rediscovers all modules with capability "indexer", dials any
// missing ones, and closes connections for modules that disappeared.
// Returns the current client set (module ID → client).
func (m *Module) syncIndexers(ctx context.Context) (map[string]indexerv1.IndexerServiceClient, error) {
	if m.testIndexerClients != nil {
		return m.testIndexerClients, nil
	}
	if m.mc == nil {
		return nil, fmt.Errorf("not connected to core")
	}
	modules, err := m.mc.Discovery.FindByCapability(ctx, "indexer")
	if err != nil {
		return nil, fmt.Errorf("discover indexer: %w", err)
	}

	wanted := make(map[string]string, len(modules)) // id → dial addr
	for _, mod := range modules {
		if mod.GetHttpAddr() == "" || mod.GetId() == "" {
			continue
		}
		wanted[mod.GetId()] = dialAddrForModule(mod.GetId(), mod.GetHttpAddr())
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("no indexer module found")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.indexerConns == nil {
		m.indexerConns = make(map[string]*grpc.ClientConn)
	}
	if m.indexerClients == nil {
		m.indexerClients = make(map[string]indexerv1.IndexerServiceClient)
	}

	for id, conn := range m.indexerConns {
		if _, ok := wanted[id]; !ok {
			conn.Close()
			delete(m.indexerConns, id)
			delete(m.indexerClients, id)
		}
	}

	for id, addr := range wanted {
		if _, ok := m.indexerClients[id]; ok {
			continue
		}
		conn, dialErr := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if dialErr != nil {
			slog.Warn("dial indexer failed", "module", id, "addr", addr, "error", dialErr)
			continue
		}
		m.indexerConns[id] = conn
		m.indexerClients[id] = indexerv1.NewIndexerServiceClient(conn)
	}

	if len(m.indexerClients) == 0 {
		return nil, fmt.Errorf("no indexer module reachable")
	}

	out := make(map[string]indexerv1.IndexerServiceClient, len(m.indexerClients))
	for id, c := range m.indexerClients {
		out[id] = c
	}
	return out, nil
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
	m.downloaderClient = cdlv1.NewDownloaderServiceClient(conn)
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

func (m *Module) ensureScanner(ctx context.Context) error {
	m.mu.RLock()
	if m.testScannerClient != nil || m.scannerClient != nil {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()

	addr, err := m.findModuleByCapability(ctx, "media.scanner")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial scanner: %w", err)
	}
	m.mu.Lock()
	m.scannerConn = conn
	m.scannerClient = scannerv1.NewScannerServiceClient(conn)
	m.mu.Unlock()
	return nil
}

func (m *Module) getScannerClient() scannerv1.ScannerServiceClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.testScannerClient != nil {
		return m.testScannerClient
	}
	return m.scannerClient
}

// ── Event Subscriptions ─────────────────────────────────────────

func (m *Module) subscribeToMediaEvents() {
	delay := 15 * time.Second
	if v := os.Getenv("AUTOMATION_EVENT_SUBSCRIBE_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			delay = d
		}
	}
	if delay > 0 {
		time.Sleep(delay)
	}
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
		contracts.EventDownloadCompleted,
		contracts.EventDownloadFailed,
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
				m.onFileAdded(context.Background(), "movie", payload.MovieID, payload.FilePath, payload.Quality)
			}
		case contracts.EventTVEpisodeFileAdded:
			var payload contracts.TVEpisodeFileAddedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.EpisodeID != "" {
				m.onFileAdded(context.Background(), "tv", payload.EpisodeID, payload.FilePath, payload.Quality)
			}
		case contracts.EventFileImported:
			var payload contracts.FileImportedPayload
			if err := json.Unmarshal(evt.Payload, &payload); err == nil && payload.OriginalPath != "" {
				slog.Info("file imported event received", "title", payload.Title, "path", payload.DestinationPath)
			}
		case contracts.EventDownloadCompleted, contracts.EventDownloadFailed:
			var payload contracts.DownloadEventPayload
			if err := json.Unmarshal(evt.Payload, &payload); err != nil {
				slog.Warn("download event unmarshal", "type", eventType, "error", err)
				continue
			}
			m.handleDownloadLifecycleEvent(context.Background(), eventType, payload)
		}
	}
	cancel()
}

// handleDownloadLifecycleEvent correlates download.completed / download.failed with
// download_history by download_id, triggers scanner ImportPath on success, and updates status.
// Owned-state updates stay on the media.file_added path — do not clear wanted here.
func (m *Module) handleDownloadLifecycleEvent(ctx context.Context, eventType string, payload contracts.DownloadEventPayload) {
	if payload.ID == "" {
		slog.Debug("download event missing id", "type", eventType)
		return
	}

	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return
	}

	var histID string
	err := db.QueryRowContext(ctx,
		`SELECT id FROM download_history WHERE download_id = ?`, payload.ID,
	).Scan(&histID)
	if err == sql.ErrNoRows {
		slog.Debug("download event for unknown download_id", "type", eventType, "download_id", payload.ID)
		return
	}
	if err != nil {
		slog.Warn("lookup download_history", "download_id", payload.ID, "error", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	if eventType == contracts.EventDownloadFailed {
		if _, err := db.ExecContext(ctx,
			`UPDATE download_history SET status = ?, completed_at = ? WHERE id = ?`,
			"failed", now, histID,
		); err != nil {
			slog.Warn("mark download failed", "id", histID, "error", err)
		}
		return
	}

	// EventDownloadCompleted
	if err := m.ensureScanner(ctx); err != nil {
		slog.Warn("ensure scanner for import", "download_id", payload.ID, "error", err)
		if _, uerr := db.ExecContext(ctx,
			`UPDATE download_history SET status = ?, completed_at = ? WHERE id = ?`,
			"import_failed", now, histID,
		); uerr != nil {
			slog.Warn("mark import_failed", "id", histID, "error", uerr)
		}
		m.publishImportFailed(payload.ID, payload.SavePath, err.Error())
		return
	}

	client := m.getScannerClient()
	if client == nil {
		slog.Warn("scanner client unavailable", "download_id", payload.ID)
		if _, uerr := db.ExecContext(ctx,
			`UPDATE download_history SET status = ?, completed_at = ? WHERE id = ?`,
			"import_failed", now, histID,
		); uerr != nil {
			slog.Warn("mark import_failed", "id", histID, "error", uerr)
		}
		m.publishImportFailed(payload.ID, payload.SavePath, "scanner client unavailable")
		return
	}

	savePath := payload.SavePath
	// Import can take a long time (large copies); never block the event loop.
	go func(histID, downloadID, savePath, now string) {
		impCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		_, err := client.ImportPath(impCtx, &scannerv1.ImportPathRequest{Path: savePath})
		if err != nil {
			slog.Warn("ImportPath failed", "download_id", downloadID, "path", savePath, "error", err)
			if _, uerr := db.ExecContext(context.Background(),
				`UPDATE download_history SET status = ?, completed_at = ? WHERE id = ?`,
				"import_failed", now, histID,
			); uerr != nil {
				slog.Warn("mark import_failed", "id", histID, "error", uerr)
			}
			m.publishImportFailed(downloadID, savePath, err.Error())
			return
		}
		if _, err := db.ExecContext(context.Background(),
			`UPDATE download_history SET status = ?, completed_at = ? WHERE id = ?`,
			"completed", now, histID,
		); err != nil {
			slog.Warn("mark download completed", "id", histID, "error", err)
		}
	}(histID, payload.ID, savePath, now)
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
	CleanTitles      []string
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
		if preferSeasonPack(len(b.items), sample.GetSeriesType()) {
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

// preferSeasonPack is false for anime so absolute-number episode searches stay episode-grain.
func preferSeasonPack(missingCount int, seriesType string) bool {
	return seasonPackEligible(missingCount) && seriesType != "anime"
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
	if e.ItemID == "" {
		return
	}
	if len(e.CleanTitles) == 0 {
		sid := e.SeriesID
		if e.ItemType == "movie" {
			sid = ""
		}
		e.CleanTitles = m.fetchCleanTitles(ctx, e.ItemType, e.ItemID, sid, e.Title)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("w_%s_%s", e.ItemType, e.ItemID)
	cleanJSON := encodeCleanTitles(e.CleanTitles)
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO wanted_items (id, item_type, item_id, tmdb_id, title, year, season_number, episode_number, monitored, missing, quality_profile_id, absolute_number, series_type, series_id, clean_titles, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?, ?, ?, ?, ?)
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
		   clean_titles = excluded.clean_titles,
		   updated_at = excluded.updated_at`,
		id, e.ItemType, e.ItemID, e.TmdbID, e.Title, e.Year,
		e.SeasonNumber, e.EpisodeNumber, e.QualityProfileID,
		e.AbsoluteNumber, e.SeriesType, e.SeriesID, cleanJSON, now, now,
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

func (m *Module) onFileAdded(ctx context.Context, itemType, itemID, filePath, quality string) {
	if itemID == "" {
		return
	}
	var profileID string
	m.mu.RLock()
	if m.db != nil {
		_ = m.db.QueryRowContext(ctx,
			`SELECT COALESCE(quality_profile_id, '') FROM wanted_items WHERE item_type = ? AND item_id = ?`,
			itemType, itemID,
		).Scan(&profileID)
	}
	m.mu.RUnlock()

	score := m.resolveOwnedScore(ctx, itemID, filePath, quality, profileID)
	p := m.loadProfileDecision(ctx, profileID)

	if !shouldKeepForUpgrade(p, score) {
		m.removeWanted(ctx, itemType, itemID)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return
	}
	res, err := m.db.ExecContext(ctx,
		`UPDATE wanted_items SET missing = 0, current_score = ?, file_acquired_at = ?, updated_at = ? WHERE item_type = ? AND item_id = ?`,
		score, now, now, itemType, itemID,
	)
	if err != nil {
		slog.Debug("mark owned wanted", "error", err, "item", itemID)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return
	}
}

func (m *Module) resolveOwnedScore(ctx context.Context, itemID, filePath, quality, profileID string) int {
	if s := m.scoreFromHistory(ctx, itemID); s > 0 {
		return s
	}
	title := quality
	if title == "" {
		title = filepath.Base(filePath)
	}
	if title == "" || title == "." {
		return 0
	}
	if err := m.ensureFormats(ctx); err == nil {
		m.mu.RLock()
		fc := m.formatsClient
		m.mu.RUnlock()
		if fc != nil {
			resp, err := fc.ScoreRelease(ctx, &formatsv1.ScoreReleaseRequest{
				Title:     title,
				ProfileId: profileID,
			})
			if err == nil && resp.GetTotalScore() > 0 {
				return int(resp.GetTotalScore())
			}
		}
	}
	return scoreRelease(&indexerv1.SearchResult{Title: title}, nil)
}

func (m *Module) scoreFromHistory(ctx context.Context, itemID string) int {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil || itemID == "" {
		return 0
	}
	var score int
	err := db.QueryRowContext(ctx,
		`SELECT score FROM download_history WHERE wanted_item_id = ? AND status IN ('completed', 'sent', 'import_failed') ORDER BY CASE status WHEN 'completed' THEN 0 WHEN 'sent' THEN 1 ELSE 2 END, created_at DESC LIMIT 1`,
		itemID,
	).Scan(&score)
	if err != nil {
		return 0
	}
	return score
}

// ── RSS Sync Loop ──────────────────────────────────────────────

func (m *Module) rssSyncLoop() {
	m.mu.RLock()
	mins := m.rssSyncMinutes
	m.mu.RUnlock()
	if mins < 1 {
		mins = 15
	}
	interval := time.Duration(mins) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	time.Sleep(10 * time.Second)
	m.rssSync()

	for range ticker.C {
		m.mu.RLock()
		enabled := m.enableAutomaticSearch
		m.mu.RUnlock()
		if !enabled {
			continue
		}
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

	type wantedRow struct {
		id, itemType, itemID, title, profileID, seriesType, seriesID, cleanRaw, fileAcquiredAt string
		tmdbID, year, seasonNum, epNum, absNum, missing, currentScore                          int64
	}

	// Drain rows before indexer RPCs so SQLite is not blocked across searches.
	rows, err := db.Query(`SELECT id, item_type, item_id, tmdb_id, title, year, season_number, episode_number, quality_profile_id, absolute_number, series_type, COALESCE(series_id, ''), COALESCE(clean_titles, '[]'), missing, current_score, COALESCE(file_acquired_at, '') FROM wanted_items WHERE monitored = 1 AND (missing = 1 OR missing = 0)`)
	if err != nil {
		slog.Error("query wanted items", "error", err)
		return
	}
	var batch []wantedRow
	for rows.Next() {
		var r wantedRow
		if err := rows.Scan(&r.id, &r.itemType, &r.itemID, &r.tmdbID, &r.title, &r.year, &r.seasonNum, &r.epNum, &r.profileID, &r.absNum, &r.seriesType, &r.seriesID, &r.cleanRaw, &r.missing, &r.currentScore, &r.fileAcquiredAt); err != nil {
			slog.Error("scan wanted row", "error", err)
			continue
		}
		batch = append(batch, r)
	}
	_ = rows.Close()

	for _, r := range batch {
		m.searchAndStore(context.Background(), r.id, r.itemType, r.itemID, r.title, int(r.tmdbID), int(r.year), int(r.seasonNum), int(r.epNum), int(r.absNum), r.seriesType, r.seriesID, r.profileID, decodeCleanTitles(r.cleanRaw), r.missing != 0, int(r.currentScore), r.fileAcquiredAt)
	}
}

func (m *Module) searchAndStore(ctx context.Context, wantedID, itemType, itemID, title string, tmdbID, year, season, episode, absolute int, seriesType, seriesID, profileID string, cleanTitles []string, missing bool, currentScore int, fileAcquiredAt string) {
	if m.hasInFlightDownload(ctx, itemID) {
		slog.Debug("skip search: in-flight download", "item", itemID)
		m.touchLastSearched(wantedID)
		return
	}

	results := m.searchWithIndexer(ctx, itemType, title, year, season, episode, absolute, seriesType, 50, profileID, cleanTitles)
	if o := m.seriesOverride(ctx, seriesID); o != nil {
		results = applyReleaseGroupOverrides(results, o.PreferredGroups, o.IgnoredGroups)
	}
	if len(results) > 0 {
		best := results[0]
		slog.Info("found releases for "+title, "item", itemID, "matches", len(results), "best", best.Title, "score", best.Score)

		p := m.loadProfileDecision(ctx, profileID)
		acquired := parseFlexibleTime(fileAcquiredAt)
		now := time.Now().UTC()
		if best.DownloadURL != "" &&
			decideGrab(p, missing, currentScore, best.Score, acquired, now) &&
			m.delayElapsed(ctx, best.GUID, best.DownloadProtocol, seriesID, now) {
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

	m.touchLastSearched(wantedID)
}

func (m *Module) touchLastSearched(wantedID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return
	}
	m.db.Exec(`UPDATE wanted_items SET last_searched = datetime('now'), updated_at = datetime('now') WHERE id = ?`, wantedID)
}

func (m *Module) hasInFlightDownload(ctx context.Context, itemID string) bool {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil || itemID == "" {
		return false
	}
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM download_history WHERE wanted_item_id = ? AND status = 'sent'`,
		itemID,
	).Scan(&n)
	return err == nil && n > 0
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

func (m *Module) scoreWithFormatsFallback(ctx context.Context, results []*indexerv1.SearchResult, title string, cleanTitles []string, profileID string) []scoredRelease {
	if len(cleanTitles) == 0 && title != "" {
		cleanTitles = []string{cleanMatchTitle(title)}
	}
	p := m.loadProfileDecision(ctx, profileID)
	var scored []scoredRelease
	if err := m.ensureFormats(ctx); err == nil {
		m.mu.RLock()
		fc := m.formatsClient
		m.mu.RUnlock()
		for _, r := range results {
			if !releaseMatchesTitles(r.GetTitle(), cleanTitles) {
				continue
			}
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
			if s > 0 && (p.MinScore <= 0 || s >= p.MinScore) {
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
		return filterByMinScore(scoreReleases(results, title, cleanTitles), p.MinScore)
	}
	sortScoredDesc(scored)
	if len(scored) > 100 {
		scored = scored[:100]
	}
	return scored
}

type profileDecision struct {
	MinScore            int
	CutoffScore         int
	UpgradeAllowed      bool
	UpgradeDelayMinutes int
}

func (m *Module) loadProfileDecision(ctx context.Context, profileID string) profileDecision {
	p := profileDecision{UpgradeAllowed: true}
	if err := m.ensureFormats(ctx); err != nil {
		return p
	}
	m.mu.RLock()
	fc := m.formatsClient
	m.mu.RUnlock()
	resp, err := fc.ListProfiles(ctx, &formatsv1.ListProfilesRequest{})
	if err != nil {
		return p
	}
	profiles := resp.GetProfiles()
	var chosen *formatsv1.QualityProfile
	if profileID != "" {
		for _, pr := range profiles {
			if pr.GetId() == profileID {
				chosen = pr
				break
			}
		}
	} else if len(profiles) > 0 {
		chosen = profiles[0]
	}
	if chosen == nil {
		return p
	}
	return profileDecision{
		MinScore:            int(chosen.GetMinScore()),
		CutoffScore:         int(chosen.GetCutoffScore()),
		UpgradeAllowed:      chosen.GetUpgradeAllowed(),
		UpgradeDelayMinutes: int(chosen.GetUpgradeDelayMinutes()),
	}
}

func decideGrab(p profileDecision, missing bool, currentScore, candidateScore int, fileAcquiredAt, now time.Time) bool {
	if candidateScore <= 0 {
		return false
	}
	if p.MinScore > 0 && candidateScore < p.MinScore {
		return false
	}
	if missing {
		return true
	}
	if !p.UpgradeAllowed {
		return false
	}
	if p.CutoffScore > 0 && currentScore >= p.CutoffScore {
		return false
	}
	if candidateScore <= currentScore {
		return false
	}
	if p.UpgradeDelayMinutes > 0 {
		if fileAcquiredAt.IsZero() {
			return false
		}
		if now.Before(fileAcquiredAt.Add(time.Duration(p.UpgradeDelayMinutes) * time.Minute)) {
			return false
		}
	}
	return true
}

func shouldKeepForUpgrade(p profileDecision, ownedScore int) bool {
	if !p.UpgradeAllowed {
		return false
	}
	if p.CutoffScore > 0 && ownedScore >= p.CutoffScore {
		return false
	}
	return true
}

func parseFlexibleTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
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

func scoreReleases(results []*indexerv1.SearchResult, title string, cleanTitles []string) []scoredRelease {
	if len(cleanTitles) == 0 && title != "" {
		cleanTitles = []string{cleanMatchTitle(title)}
	}
	var scored []scoredRelease
	for _, r := range results {
		s := scoreRelease(r, cleanTitles)
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

func scoreRelease(r *indexerv1.SearchResult, cleanTitles []string) int {
	name := r.GetTitle()
	if !releaseMatchesTitles(name, cleanTitles) {
		return 0
	}
	score := 0

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
		int(req.GetYear()), int(req.GetSeason()), int(req.GetEpisode()),
		int(req.GetAbsolute()), req.GetSeriesType(), int(req.GetLimit()), req.GetQualityProfileId(),
		[]string{cleanMatchTitle(req.GetQuery())})

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

func (m *Module) searchWithIndexer(ctx context.Context, itemType, query string, year, season, episode, absolute int, seriesType string, limit int, profileID string, cleanTitles []string) []scoredRelease {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	clients, err := m.syncIndexers(ctx)
	if err != nil {
		slog.Debug("no indexer available", "error", err)
		return nil
	}

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

	req := &indexerv1.SearchRequest{
		Query:    searchQuery,
		Type:     searchType,
		Year:     int32(year),
		Season:   int32(season),
		Episode:  int32(episode),
		Absolute: int32(absolute),
		Limit:    maxLimit,
	}

	raw := parallelIndexerSearch(ctx, clients, req)
	if len(raw) == 0 {
		return nil
	}

	scored := m.scoreWithFormatsFallback(ctx, raw, query, cleanTitles, profileID)
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

	scored = dedupeScoredReleases(scored)
	if int(maxLimit) > 0 && len(scored) > int(maxLimit) {
		scored = scored[:maxLimit]
	}
	return scored
}

// parallelIndexerSearch fans out Search to every client; failures are logged and skipped.
func parallelIndexerSearch(ctx context.Context, clients map[string]indexerv1.IndexerServiceClient, req *indexerv1.SearchRequest) []*indexerv1.SearchResult {
	if len(clients) == 0 {
		return nil
	}
	type batch struct {
		id      string
		results []*indexerv1.SearchResult
		err     error
	}
	ch := make(chan batch, len(clients))
	var wg sync.WaitGroup
	for id, client := range clients {
		wg.Add(1)
		go func(id string, client indexerv1.IndexerServiceClient) {
			defer wg.Done()
			resp, err := client.Search(ctx, req)
			if err != nil {
				ch <- batch{id: id, err: err}
				return
			}
			ch <- batch{id: id, results: resp.GetResults()}
		}(id, client)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	var all []*indexerv1.SearchResult
	for b := range ch {
		if b.err != nil {
			slog.Warn("indexer search failed", "module", b.id, "error", b.err)
			continue
		}
		all = append(all, b.results...)
	}
	return all
}

// dedupeScoredReleases keeps the highest-scored release per guid (or download_url if guid empty).
// Input should already be sorted descending by score for stable preference; if not, higher score still wins.
func dedupeScoredReleases(scored []scoredRelease) []scoredRelease {
	if len(scored) <= 1 {
		return scored
	}
	best := make(map[string]scoredRelease, len(scored))
	order := make([]string, 0, len(scored))
	for _, r := range scored {
		key := r.GUID
		if key == "" {
			key = r.DownloadURL
		}
		if key == "" {
			// No stable identity — keep as unique by appending synthetic key.
			key = fmt.Sprintf("_anon_%d_%s", len(order), r.Title)
		}
		if prev, ok := best[key]; ok {
			if r.Score > prev.Score {
				best[key] = r
			}
			continue
		}
		best[key] = r
		order = append(order, key)
	}
	out := make([]scoredRelease, 0, len(order))
	for _, key := range order {
		out = append(out, best[key])
	}
	sortScoredDesc(out)
	return out
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
	db := m.db
	m.mu.RUnlock()
	if client == nil {
		return nil, fmt.Errorf("no downloader available")
	}

	// Do not inherit the caller's deadline for AddTorrent: completion-side
	// ImportPath can run for minutes and must not cancel magnet handoff.
	addCtx, addCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer addCancel()
	addResp, err := client.AddTorrent(addCtx, &cdlv1.AddTorrentRequest{
		TorrentUrl: req.GetDownloadUrl(),
		Category:   req.GetItemType(),
	})
	if err != nil {
		return nil, fmt.Errorf("add torrent: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	histID := fmt.Sprintf("dl_%s_%d", req.GetGuid(), time.Now().UnixNano())
	if db != nil {
		if _, err := db.ExecContext(context.Background(),
			`INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'sent', ?, ?, ?)`,
			histID,
			req.GetItemId(), req.GetGuid(), req.GetTitle(), req.GetIndexerName(),
			req.GetSize(), req.GetScore(), req.GetDownloadUrl(), req.GetDownloadProtocol(),
			now, now, addResp.GetTorrentId(),
		); err != nil {
			slog.Warn("insert download_history", "error", err)
		}
	}
	var seriesID string
	if db != nil && req.GetItemType() == "tv" && req.GetItemId() != "" {
		_ = db.QueryRowContext(context.Background(),
			`SELECT COALESCE(series_id, '') FROM wanted_items WHERE item_type = ? AND item_id = ? LIMIT 1`,
			req.GetItemType(), req.GetItemId(),
		).Scan(&seriesID)
	}

	payload, _ := json.Marshal(contracts.DownloadDispatchedPayload{
		Title:            req.GetTitle(),
		DownloadProtocol: req.GetDownloadProtocol(),
		Score:            req.GetScore(),
		ItemType:         req.GetItemType(),
		ItemID:           req.GetItemId(),
		TMDBID:           req.GetTmdbId(),
		GUID:             req.GetGuid(),
		Indexer:          req.GetIndexerName(),
		Size:             req.GetSize(),
		DownloadID:       addResp.GetTorrentId(),
		SeriesID:         seriesID,
	})
	// Publish async so a slow bus/policy path cannot block the Dispatch RPC.
	go m.publishEvent(context.Background(), contracts.EventDownloadDispatched, payload)

	slog.Info("dispatched download", "title", req.GetTitle(), "protocol", req.GetDownloadProtocol(), "id", addResp.GetTorrentId())
	return &automationv1.DispatchResponse{
		DownloadId: addResp.GetTorrentId(),
		Status:     "sent",
	}, nil
}


func (m *Module) publishImportFailed(downloadID, path, errMsg string) {
	payload, err := json.Marshal(contracts.ImportFailedPayload{
		DownloadID: downloadID,
		Path:       path,
		Error:      errMsg,
	})
	if err != nil {
		slog.Warn("marshal import failed payload", "error", err)
		return
	}
	go m.publishEvent(context.Background(), contracts.EventImportFailed, payload)
}

func (m *Module) publishEvent(ctx context.Context, eventType string, payload []byte) {
	if m.mc == nil {
		return
	}
	if err := m.mc.Events.Publish(ctx, eventType, m.id, payload); err != nil {
		slog.Warn("publish event failed", "type", eventType, "error", err)
	}
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
		AbsoluteNumber:   req.GetAbsoluteNumber(),
		SeriesType:       req.GetSeriesType(),
		SeriesID:         req.GetSeriesId(),
	})
	id := fmt.Sprintf("w_%s_%s", req.GetItemType(), req.GetItemId())
	return &automationv1.AddToQueueResponse{QueueId: id}, nil
}

func (m *Module) GetQueue(ctx context.Context, req *automationv1.GetQueueRequest) (*automationv1.GetQueueResponse, error) {
	// Copy db under lock then release so writers (library sync / ImportPath bookkeeping)
	// cannot stall the admin UI for the duration of the query.
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
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
	_ = db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	query += ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	qargs := append(args, pageSize, offset)

	rows, err := db.QueryContext(ctx, query, qargs...)
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
	db := m.db
	m.mu.RUnlock()
	if db == nil {
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
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_history`).Scan(&total)

	rows, err := db.QueryContext(ctx,
		`SELECT id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, completed_at, created_at, download_id FROM download_history ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		pageSize, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	var records []*automationv1.DownloadRecord
	for rows.Next() {
		var id, wantedItemID, guid, title, indexer, downloadURL, downloadProto, status, createdAt, downloadID string
		var size, score int64
		var sentAt, completedAt sql.NullString
		if err := rows.Scan(&id, &wantedItemID, &guid, &title, &indexer, &size, &score, &downloadURL, &downloadProto, &status, &sentAt, &completedAt, &createdAt, &downloadID); err != nil {
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
			DownloadId: downloadID,
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
