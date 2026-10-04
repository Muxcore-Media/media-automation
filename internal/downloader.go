package internal

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	usenetv1 "github.com/Muxcore-Media/downloader-sabnzbd/proto/gen/muxcore/usenet/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// maxDispatchFailover caps how many other downloaders one Dispatch tries after
// the active one is unreachable.
const maxDispatchFailover = 2

// downloaderConn is one dialed downloader module. Both service stubs share the
// connection; callers use the one matching the module's protocol.
type downloaderConn struct {
	id      string
	addr    string
	conn    *grpc.ClientConn
	torrent cdlv1.DownloaderServiceClient
	usenet  usenetv1.UsenetDownloaderServiceClient
}

// downloaderCandidate is a downloader module discovered on the mesh.
type downloaderCandidate struct {
	id   string
	addr string
}

// downloaderPool keeps one connection per downloader module and remembers the
// active module per protocol. New grabs go to the active module; status and
// removal calls go to whichever module a download was sent to, so failing over
// to another client never strands downloads already on the first one.
type downloaderPool struct {
	mu sync.RWMutex

	conns           map[string]*downloaderConn
	torrentModuleID string
	usenetModuleID  string

	// discover and dial are overridable in tests.
	discover func(ctx context.Context, capability string) ([]downloaderCandidate, error)
	dial     func(id, addr string) (*downloaderConn, error)
}

func dialDownloader(id, addr string) (*downloaderConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial downloader %s: %w", id, err)
	}
	return &downloaderConn{
		id:      id,
		addr:    addr,
		conn:    conn,
		torrent: cdlv1.NewDownloaderServiceClient(conn),
		usenet:  usenetv1.NewUsenetDownloaderServiceClient(conn),
	}, nil
}

func (p *downloaderPool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, c := range p.conns {
		if c.conn != nil {
			_ = c.conn.Close()
		}
		delete(p.conns, id)
	}
	p.torrentModuleID = ""
	p.usenetModuleID = ""
}

func (m *Module) torrentClientLocked() cdlv1.DownloaderServiceClient {
	if m.downloaderClient != nil {
		return m.downloaderClient
	}
	if m.testTorrentClient != nil {
		return m.testTorrentClient
	}
	m.downloaderPool.mu.RLock()
	defer m.downloaderPool.mu.RUnlock()
	if c := m.downloaderPool.conns[m.downloaderPool.torrentModuleID]; c != nil {
		return c.torrent
	}
	return nil
}

func (m *Module) usenetClientLocked() usenetv1.UsenetDownloaderServiceClient {
	if m.testUsenetClient != nil {
		return m.testUsenetClient
	}
	m.downloaderPool.mu.RLock()
	defer m.downloaderPool.mu.RUnlock()
	if c := m.downloaderPool.conns[m.downloaderPool.usenetModuleID]; c != nil {
		return c.usenet
	}
	return nil
}

// activeDownloader returns the module new grabs for protocol go to ("" when a
// test client is injected or nothing is connected yet).
func (m *Module) activeDownloader(protocol string) string {
	if isUsenetProtocol(protocol) {
		if m.testUsenetClient != nil {
			return ""
		}
	} else if m.downloaderClient != nil || m.testTorrentClient != nil {
		return ""
	}
	m.downloaderPool.mu.RLock()
	defer m.downloaderPool.mu.RUnlock()
	if isUsenetProtocol(protocol) {
		return m.downloaderPool.usenetModuleID
	}
	return m.downloaderPool.torrentModuleID
}

func (m *Module) ensureTorrentDownloader(ctx context.Context) error {
	return m.ensureDownloaderForProtocol(ctx, "torrent")
}

func (m *Module) ensureUsenetDownloader(ctx context.Context) error {
	return m.ensureDownloaderForProtocol(ctx, "usenet")
}

// ensureDownloaderForProtocol makes sure an active downloader is connected for
// protocol, picking the first candidate that is not benched.
func (m *Module) ensureDownloaderForProtocol(ctx context.Context, protocol string) error {
	usenet := isUsenetProtocol(protocol)
	if usenet && m.usenetClientLocked() != nil || !usenet && m.torrentClientLocked() != nil {
		return nil
	}
	candidates, err := m.downloaderCandidates(ctx, usenet)
	if err != nil {
		if usenet {
			return fmt.Errorf("no usenet downloader: %w", err)
		}
		return err
	}
	ids := make([]string, len(candidates))
	byID := make(map[string]downloaderCandidate, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
		byID[c.id] = c
	}
	var lastErr error
	for _, id := range m.downloaderHealth.filterUsable(ids) {
		c, err := m.downloaderConnFor(byID[id])
		if err != nil {
			lastErr = err
			continue
		}
		m.downloaderPool.mu.Lock()
		if usenet {
			m.downloaderPool.usenetModuleID = c.id
		} else {
			m.downloaderPool.torrentModuleID = c.id
		}
		m.downloaderPool.mu.Unlock()
		slog.Info("connected downloader", "protocol", protocol, "module", c.id, "addr", c.addr)
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no downloader module reachable")
	}
	return lastErr
}

// failoverDownloader benches the active module for protocol after it was
// unreachable and switches to the next usable candidate. It reports whether a
// different module is now active.
func (m *Module) failoverDownloader(ctx context.Context, protocol, failedID string, cause error) bool {
	m.downloaderHealth.bench(failedID, cause)
	m.downloaderPool.mu.Lock()
	if isUsenetProtocol(protocol) {
		if m.downloaderPool.usenetModuleID == failedID {
			m.downloaderPool.usenetModuleID = ""
		}
	} else if m.downloaderPool.torrentModuleID == failedID {
		m.downloaderPool.torrentModuleID = ""
	}
	m.downloaderPool.mu.Unlock()
	if err := m.ensureDownloaderForProtocol(ctx, protocol); err != nil {
		return false
	}
	next := m.activeDownloader(protocol)
	if next == "" || next == failedID {
		return false
	}
	slog.Warn("downloader failover", "protocol", protocol, "from", failedID, "to", next, "error", cause)
	return true
}

// downloaderConnFor returns the cached connection for a candidate, dialing it
// on first use.
func (m *Module) downloaderConnFor(c downloaderCandidate) (*downloaderConn, error) {
	m.downloaderPool.mu.Lock()
	defer m.downloaderPool.mu.Unlock()
	if existing := m.downloaderPool.conns[c.id]; existing != nil {
		if existing.addr == c.addr || c.addr == "" {
			return existing, nil
		}
		// Module moved; redial.
		if existing.conn != nil {
			_ = existing.conn.Close()
		}
		delete(m.downloaderPool.conns, c.id)
	}
	dial := m.downloaderPool.dial
	if dial == nil {
		dial = dialDownloader
	}
	conn, err := dial(c.id, c.addr)
	if err != nil {
		return nil, err
	}
	if m.downloaderPool.conns == nil {
		m.downloaderPool.conns = make(map[string]*downloaderConn)
	}
	m.downloaderPool.conns[c.id] = conn
	return conn, nil
}

// downloaderConnByID resolves a module a download was previously sent to.
func (m *Module) downloaderConnByID(ctx context.Context, moduleID string, usenet bool) *downloaderConn {
	m.downloaderPool.mu.RLock()
	c := m.downloaderPool.conns[moduleID]
	m.downloaderPool.mu.RUnlock()
	if c != nil {
		return c
	}
	candidates, err := m.downloaderCandidates(ctx, usenet)
	if err != nil {
		return nil
	}
	for _, cand := range candidates {
		if cand.id != moduleID {
			continue
		}
		conn, err := m.downloaderConnFor(cand)
		if err != nil {
			slog.Debug("dial downloader for existing download", "module", moduleID, "error", err)
			return nil
		}
		return conn
	}
	return nil
}

// torrentClientFor returns the client for a torrent previously sent to
// moduleID. An empty moduleID (rows from before modules were recorded) uses
// the active client.
func (m *Module) torrentClientFor(ctx context.Context, moduleID string) cdlv1.DownloaderServiceClient {
	if m.downloaderClient != nil || m.testTorrentClient != nil || moduleID == "" || moduleID == m.activeDownloader("torrent") {
		client := m.torrentClientLocked()
		if client == nil {
			_ = m.ensureTorrentDownloader(ctx)
			client = m.torrentClientLocked()
		}
		return client
	}
	if c := m.downloaderConnByID(ctx, moduleID, false); c != nil {
		return c.torrent
	}
	return nil
}

// usenetClientFor is torrentClientFor for usenet jobs.
func (m *Module) usenetClientFor(ctx context.Context, moduleID string) usenetv1.UsenetDownloaderServiceClient {
	if m.testUsenetClient != nil || moduleID == "" || moduleID == m.activeDownloader("usenet") {
		client := m.usenetClientLocked()
		if client == nil {
			_ = m.ensureUsenetDownloader(ctx)
			client = m.usenetClientLocked()
		}
		return client
	}
	if c := m.downloaderConnByID(ctx, moduleID, true); c != nil {
		return c.usenet
	}
	return nil
}

// downloaderCandidates lists downloader modules for a protocol in preference
// order: the AUTOMATION_DOWNLOADER_* module first, then native clients, then
// bridges (qBittorrent for torrent, SABnzbd for usenet).
func (m *Module) downloaderCandidates(ctx context.Context, usenet bool) ([]downloaderCandidate, error) {
	discover := m.downloaderPool.discover
	if discover == nil {
		discover = m.discoverDownloaders
	}
	type pass struct{ capability, prefEnv string }
	passes := []pass{
		{"downloader.torrent", "AUTOMATION_DOWNLOADER_TORRENT"},
		{"downloader", "AUTOMATION_DOWNLOADER_TORRENT"},
	}
	if usenet {
		passes = []pass{{"downloader.usenet", "AUTOMATION_DOWNLOADER_USENET"}}
	}
	var out []downloaderCandidate
	seen := make(map[string]bool)
	var lastErr error
	for _, p := range passes {
		found, err := discover(ctx, p.capability)
		if err != nil {
			lastErr = fmt.Errorf("discover %s: %w", p.capability, err)
			continue
		}
		for _, c := range orderDownloaderCandidates(p.capability, strings.TrimSpace(os.Getenv(p.prefEnv)), found) {
			if !seen[c.id] {
				seen[c.id] = true
				out = append(out, c)
			}
		}
	}
	if len(out) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		if usenet {
			return nil, fmt.Errorf("no downloader.usenet module found")
		}
		return nil, fmt.Errorf("no downloader module found")
	}
	return out, nil
}

func (m *Module) discoverDownloaders(ctx context.Context, capability string) ([]downloaderCandidate, error) {
	if m.mc == nil {
		return nil, fmt.Errorf("not connected to core")
	}
	ctx, cancel := withPeerTimeout(ctx, peerDiscoveryTimeout)
	defer cancel()
	modules, err := m.mc.Discovery.FindByCapability(ctx, capability)
	if err != nil {
		return nil, err
	}
	out := make([]downloaderCandidate, 0, len(modules))
	for _, mod := range modules {
		if mod.GetId() == "" || mod.GetHttpAddr() == "" {
			continue
		}
		out = append(out, downloaderCandidate{id: mod.GetId(), addr: dialAddrForModule(mod.GetId(), mod.GetHttpAddr())})
	}
	return out, nil
}

// orderDownloaderCandidates applies the preference rules for one capability:
// the preferred module first, then the rest in discovery order with bridge
// modules held back (qBittorrent is reached via the generic "downloader"
// capability; SABnzbd goes after the native usenet client).
func orderDownloaderCandidates(capability, pref string, modules []downloaderCandidate) []downloaderCandidate {
	var out []downloaderCandidate
	if pref != "" {
		found := false
		for _, mod := range modules {
			if mod.id == pref {
				out = append(out, mod)
				found = true
				break
			}
		}
		if !found {
			slog.Warn("preferred downloader module not found", "capability", capability, "pref", pref)
		}
	}
	var held []downloaderCandidate
	for _, mod := range modules {
		if mod.id == pref {
			continue
		}
		switch {
		case capability == "downloader" && strings.Contains(mod.id, "sabnzbd"):
			continue
		case capability == "downloader.torrent" && mod.id == "downloader-qbittorrent":
			continue
		case capability == "downloader.usenet" && mod.id == "downloader-sabnzbd":
			held = append(held, mod)
			continue
		}
		out = append(out, mod)
	}
	return append(out, held...)
}

func (m *Module) downloaderClientLocked() cdlv1.DownloaderServiceClient {
	return m.torrentClientLocked()
}

func (m *Module) ensureDownloader(ctx context.Context) error {
	return m.ensureTorrentDownloader(ctx)
}
