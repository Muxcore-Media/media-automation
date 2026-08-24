package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type actionsFile struct {
	Remove   []removeAction `json:"remove"`
	Research []researchJob  `json:"research"`
}

type removeAction struct {
	DownloadID  string `json:"download_id"`
	DeleteFiles bool   `json:"delete_files"`
	Want        string `json:"want"`
	Release     string `json:"release"`
	Status      string `json:"status"`
}

type researchJob struct {
	ItemType string `json:"item_type"`
	ItemID   string `json:"item_id"`
	Title    string `json:"title"`
	Year     int32  `json:"year"`
	Season   int32  `json:"season"`
	Episode  int32  `json:"episode"`
	TMDBID   int32  `json:"tmdb_id"`
}

func main() {
	dlAddr := flag.String("dl", "127.0.0.1:9461", "downloader gRPC address")
	autoAddr := flag.String("auto", "127.0.0.1:9460", "automation gRPC address")
	jsonPath := flag.String("json", "/tmp/grab-actions.json", "actions JSON")
	listOnly := flag.Bool("list", false, "only list torrents")
	apply := flag.Bool("apply", false, "remove torrents and re-search")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	dlConn, err := grpc.NewClient(*dlAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fatalf("dial downloader: %v", err)
	}
	defer func() { _ = dlConn.Close() }()
	dl := cdlv1.NewDownloaderServiceClient(dlConn)

	list, err := dl.ListTorrents(ctx, &cdlv1.ListTorrentsRequest{})
	if err != nil {
		fatalf("ListTorrents: %v", err)
	}
	fmt.Printf("=== torrents (%d) ===\n", len(list.GetTorrents()))
	for _, t := range list.GetTorrents() {
		fmt.Printf("%s  %s  %d/%d  %s\n", t.GetId(), t.GetStatus(), t.GetDownloaded(), t.GetSize(), t.GetName())
	}
	if *listOnly {
		return
	}

	raw, err := os.ReadFile(*jsonPath)
	if err != nil {
		fatalf("read json: %v", err)
	}
	var actions actionsFile
	if err := json.Unmarshal(raw, &actions); err != nil {
		fatalf("parse json: %v", err)
	}

	if !*apply {
		fmt.Printf("dry-run: would remove %d torrents, research %d items (pass -apply)\n", len(actions.Remove), len(actions.Research))
		for _, r := range actions.Remove {
			fmt.Printf("  REMOVE delete_files=%v status=%s want=%q rel=%q id=%s\n", r.DeleteFiles, r.Status, r.Want, r.Release, r.DownloadID)
		}
		for _, j := range actions.Research {
			fmt.Printf("  RESEARCH %s %q y=%d S%dE%d item=%s\n", j.ItemType, j.Title, j.Year, j.Season, j.Episode, j.ItemID)
		}
		return
	}

	seen := map[string]bool{}
	for _, r := range actions.Remove {
		if r.DownloadID == "" || seen[r.DownloadID] {
			continue
		}
		seen[r.DownloadID] = true
		rmCtx, rmCancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := dl.RemoveTorrent(rmCtx, &cdlv1.RemoveTorrentRequest{
			TorrentId:   r.DownloadID,
			DeleteFiles: r.DeleteFiles,
		})
		rmCancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "RemoveTorrent %s: %v\n", r.DownloadID, err)
			continue
		}
		fmt.Printf("removed %s delete_files=%v (%s)\n", r.DownloadID, r.DeleteFiles, r.Release)
	}

	autoConn, err := grpc.NewClient(*autoAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fatalf("dial automation: %v", err)
	}
	defer func() { _ = autoConn.Close() }()
	auto := automationv1.NewAutomationServiceClient(autoConn)

	for _, j := range actions.Research {
		fmt.Printf("search %s %q y=%d S%dE%d\n", j.ItemType, j.Title, j.Year, j.Season, j.Episode)
		sctx, scancel := context.WithTimeout(ctx, 45*time.Second)
		resp, err := auto.SearchItem(sctx, &automationv1.SearchItemRequest{
			ItemType: j.ItemType,
			Query:    j.Title,
			TmdbId:   j.TMDBID,
			Year:     j.Year,
			Season:   j.Season,
			Episode:  j.Episode,
			Limit:    20,
		})
		scancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  SearchItem: %v\n", err)
			continue
		}
		matches := resp.GetMatches()
		if len(matches) == 0 {
			fmt.Printf("  no matches\n")
			continue
		}
		best := matches[0]
		fmt.Printf("  best score=%d %s\n", best.GetScore(), best.GetTitle())
		dctx, dcancel := context.WithTimeout(ctx, 45*time.Second)
		dresp, err := auto.Dispatch(dctx, &automationv1.DispatchRequest{
			Guid:             best.GetGuid(),
			Title:            best.GetTitle(),
			DownloadUrl:      best.GetDownloadUrl(),
			DownloadProtocol: best.GetDownloadProtocol(),
			Size:             best.GetSize(),
			Score:            best.GetScore(),
			IndexerName:      best.GetIndexerName(),
			ItemType:         j.ItemType,
			ItemId:           j.ItemID,
			TmdbId:           j.TMDBID,
		})
		dcancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Dispatch: %v\n", err)
			continue
		}
		fmt.Printf("  dispatched id=%s status=%s\n", dresp.GetDownloadId(), dresp.GetStatus())
		time.Sleep(1500 * time.Millisecond)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
