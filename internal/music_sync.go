package internal

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	musicv1 "github.com/Muxcore-Media/media-music/proto/gen/muxcore/music/v1"
)

func (m *Module) ensureMusic(ctx context.Context) error {
	m.mu.RLock()
	if m.musicClient != nil {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()

	addr, err := m.findModuleByCapability(ctx, "media.library.music")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial music: %w", err)
	}
	m.mu.Lock()
	m.musicConn = conn
	m.musicClient = musicv1.NewMusicManagementServiceClient(conn)
	m.mu.Unlock()
	return nil
}

func (m *Module) syncWantedMusic(ctx context.Context, seen map[string]struct{}) (int, error) {
	if err := m.ensureMusic(ctx); err != nil {
		return 0, err
	}
	m.mu.RLock()
	client := m.musicClient
	m.mu.RUnlock()

	page := int32(1)
	totalUpserted := 0
	for {
		resp, err := client.ListMissing(ctx, &musicv1.ListMissingRequest{Page: page, PageSize: 100})
		if err != nil {
			return totalUpserted, err
		}
		for _, item := range resp.GetItems() {
			key := "music:" + item.GetAlbumId()
			if seen != nil {
				seen[key] = struct{}{}
			}
			title := item.GetTitle()
			if item.GetArtistName() != "" {
				title = item.GetArtistName() + " - " + item.GetTitle()
			}
			m.upsertWanted(ctx, wantedEntry{
				ItemType: "music", ItemID: item.GetAlbumId(),
				Title: title, Year: item.GetYear(),
				QualityProfileID: item.GetQualityProfileId(),
				SeriesID:         item.GetArtistId(),
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

func (m *Module) logMusicSync(n int, ok bool, err error) {
	if err != nil {
		slog.Debug("sync missing music albums", "error", err)
		return
	}
	if ok {
		slog.Debug("sync missing music albums upserted", "albums", n)
	}
}
