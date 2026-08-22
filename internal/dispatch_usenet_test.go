package internal

import (
	"context"
	"testing"

	usenetv1 "github.com/Muxcore-Media/downloader-sabnzbd/proto/gen/muxcore/usenet/v1"
	autov1 "github.com/Muxcore-Media/media-automation/proto/automationv1"
	"google.golang.org/grpc"
)

type fakeUsenetClient struct {
	usenetv1.UsenetDownloaderServiceClient
	jobID string
	calls int
	last  *usenetv1.AddNZBRequest
}

func (f *fakeUsenetClient) AddNZB(ctx context.Context, in *usenetv1.AddNZBRequest, opts ...grpc.CallOption) (*usenetv1.AddNZBResponse, error) {
	f.calls++
	f.last = in
	id := f.jobID
	if id == "" {
		id = "nzb-fixture-1"
	}
	return &usenetv1.AddNZBResponse{JobId: id, Name: in.GetName()}, nil
}

func TestDispatchUsenet(t *testing.T) {
	m := newTestModule(t)
	fake := &fakeUsenetClient{jobID: "job-usenet-1"}
	m.testUsenetClient = fake

	resp, err := m.Dispatch(context.Background(), &autov1.DispatchRequest{
		Guid:             "guid-nzb-1",
		Title:            "Fight Club",
		DownloadUrl:      "https://indexer.example/get.php?guid=abc&id=1",
		DownloadProtocol: "usenet",
		ItemType:         "movie",
		ItemId:           "mv_550",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetDownloadId() != "job-usenet-1" {
		t.Fatalf("download id: %s", resp.GetDownloadId())
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 AddNZB, got %d", fake.calls)
	}
	if fake.last.GetNzbUrl() == "" {
		t.Fatal("expected nzb url")
	}
}

func TestRefineGrabRankingPrefersUsenetForDeadTorrent(t *testing.T) {
	in := []scoredRelease{
		{GUID: "t1", Title: "A.1080p", Score: 100, Seeders: 0, DownloadProtocol: "torrent", DownloadURL: "magnet:1"},
		{GUID: "u1", Title: "A.1080p", Score: 100, DownloadProtocol: "usenet", DownloadURL: "https://x/nzb?id=1"},
	}
	out := refineGrabRanking(in, true)
	if len(out) != 2 {
		t.Fatalf("len %d", len(out))
	}
	if !isUsenetProtocol(out[0].DownloadProtocol) {
		t.Fatalf("expected usenet first, got %+v", out[0])
	}
}

func TestNormalizeProtocol(t *testing.T) {
	if normalizeProtocol("nzb") != "usenet" {
		t.Fatal("nzb -> usenet")
	}
	if detectProtocolFromURL("https://host/file.nzb") != "usenet" {
		t.Fatal(".nzb url")
	}
}
