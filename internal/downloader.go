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

type downloaderPool struct {
	mu sync.RWMutex

	torrentModuleID string
	torrentAddr     string
	torrentConn     *grpc.ClientConn
	torrentClient   cdlv1.DownloaderServiceClient

	usenetModuleID string
	usenetAddr     string
	usenetConn     *grpc.ClientConn
	usenetClient   usenetv1.UsenetDownloaderServiceClient
}

func (m *Module) torrentClientLocked() cdlv1.DownloaderServiceClient {
	if m.downloaderClient != nil {
		return m.downloaderClient
	}
	m.downloaderPool.mu.RLock()
	defer m.downloaderPool.mu.RUnlock()
	if m.testTorrentClient != nil {
		return m.testTorrentClient
	}
	return m.downloaderPool.torrentClient
}

func (m *Module) usenetClientLocked() usenetv1.UsenetDownloaderServiceClient {
	m.downloaderPool.mu.RLock()
	defer m.downloaderPool.mu.RUnlock()
	if m.testUsenetClient != nil {
		return m.testUsenetClient
	}
	return m.downloaderPool.usenetClient
}

func (m *Module) ensureTorrentDownloader(ctx context.Context) error {
	if m.torrentClientLocked() != nil {
		return nil
	}
	m.downloaderPool.mu.RLock()
	if m.downloaderPool.torrentClient != nil {
		m.downloaderPool.mu.RUnlock()
		return nil
	}
	m.downloaderPool.mu.RUnlock()

	moduleID, addr, err := m.findPreferredModule(ctx, "downloader.torrent", "AUTOMATION_DOWNLOADER_TORRENT")
	if err != nil {
		moduleID, addr, err = m.findPreferredModule(ctx, "downloader", "AUTOMATION_DOWNLOADER_TORRENT")
		if err != nil {
			return err
		}
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial torrent downloader %s: %w", moduleID, err)
	}
	m.downloaderPool.mu.Lock()
	defer m.downloaderPool.mu.Unlock()
	if m.downloaderPool.torrentClient != nil {
		_ = conn.Close()
		return nil
	}
	m.downloaderPool.torrentConn = conn
	m.downloaderPool.torrentClient = cdlv1.NewDownloaderServiceClient(conn)
	m.downloaderPool.torrentModuleID = moduleID
	m.downloaderPool.torrentAddr = addr
	slog.Info("connected torrent downloader", "module", moduleID, "addr", addr)
	return nil
}

func (m *Module) ensureUsenetDownloader(ctx context.Context) error {
	if m.usenetClientLocked() != nil {
		return nil
	}
	m.downloaderPool.mu.RLock()
	if m.downloaderPool.usenetClient != nil {
		m.downloaderPool.mu.RUnlock()
		return nil
	}
	m.downloaderPool.mu.RUnlock()

	moduleID, addr, err := m.findPreferredModule(ctx, "downloader.usenet", "AUTOMATION_DOWNLOADER_USENET")
	if err != nil {
		return fmt.Errorf("no usenet downloader: %w", err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial usenet downloader %s: %w", moduleID, err)
	}
	m.downloaderPool.mu.Lock()
	defer m.downloaderPool.mu.Unlock()
	if m.downloaderPool.usenetClient != nil {
		_ = conn.Close()
		return nil
	}
	m.downloaderPool.usenetConn = conn
	m.downloaderPool.usenetClient = usenetv1.NewUsenetDownloaderServiceClient(conn)
	m.downloaderPool.usenetModuleID = moduleID
	m.downloaderPool.usenetAddr = addr
	slog.Info("connected usenet downloader", "module", moduleID, "addr", addr)
	return nil
}

func (m *Module) ensureDownloaderForProtocol(ctx context.Context, protocol string) error {
	if isUsenetProtocol(protocol) {
		return m.ensureUsenetDownloader(ctx)
	}
	return m.ensureTorrentDownloader(ctx)
}

func (m *Module) findPreferredModule(ctx context.Context, capability, prefEnv string) (moduleID, addr string, err error) {
	if m.mc == nil {
		return "", "", fmt.Errorf("not connected to core")
	}
	ctx, cancel := withPeerTimeout(ctx, peerDiscoveryTimeout)
	defer cancel()
	modules, err := m.mc.Discovery.FindByCapability(ctx, capability)
	if err != nil {
		return "", "", fmt.Errorf("discover %s: %w", capability, err)
	}
	pref := strings.TrimSpace(os.Getenv(prefEnv))
	if pref != "" {
		for _, mod := range modules {
			if mod.GetId() == pref && mod.GetHttpAddr() != "" {
				return pref, dialAddrForModule(pref, mod.GetHttpAddr()), nil
			}
		}
		slog.Warn("preferred downloader module not found", "capability", capability, "pref", pref)
	}
	for _, mod := range modules {
		if mod.GetHttpAddr() == "" || mod.GetId() == "" {
			continue
		}
		if capability == "downloader" && strings.Contains(mod.GetId(), "sabnzbd") {
			continue
		}
		if capability == "downloader.torrent" && mod.GetId() == "downloader-qbittorrent" {
			continue
		}
		if capability == "downloader.usenet" && mod.GetId() == "downloader-sabnzbd" {
			continue
		}
		return mod.GetId(), dialAddrForModule(mod.GetId(), mod.GetHttpAddr()), nil
	}
	// Prefer native usenet over SABnzbd bridge when both are registered.
	if capability == "downloader.usenet" {
		for _, mod := range modules {
			if mod.GetId() == "downloader-sabnzbd" && mod.GetHttpAddr() != "" {
				return mod.GetId(), dialAddrForModule(mod.GetId(), mod.GetHttpAddr()), nil
			}
		}
	}
	return "", "", fmt.Errorf("no %s module found", capability)
}

func (m *Module) downloaderClientLocked() cdlv1.DownloaderServiceClient {
	return m.torrentClientLocked()
}

func (m *Module) ensureDownloader(ctx context.Context) error {
	return m.ensureTorrentDownloader(ctx)
}
