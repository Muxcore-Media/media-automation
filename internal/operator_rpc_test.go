package internal

import (
	"context"
	"testing"

	autov1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
)

func TestRemoveFromQueueAndBlocklist(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType: "movie", ItemId: "mv1", Title: "Test", Year: 2020,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.RemoveFromQueue(ctx, &autov1.RemoveFromQueueRequest{QueueId: add.QueueId}); err != nil {
		t.Fatal(err)
	}
	q, err := m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Items) != 0 {
		t.Fatalf("expected empty queue, got %d", len(q.Items))
	}

	m.blacklistRelease(ctx, "w_movie_mv1", "guid-1", 1, "stall")
	list, err := m.ListBlocklist(ctx, &autov1.ListBlocklistRequest{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 {
		t.Fatalf("expected 1 blocklist entry, got %d", list.Total)
	}
	if _, err := m.BlocklistRelease(ctx, &autov1.BlocklistReleaseRequest{
		WantedItemId: "w_movie_mv2", Guid: "guid-2", Reason: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	list, err = m.ListBlocklist(ctx, &autov1.ListBlocklistRequest{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("expected 2 blocklist entries after BlocklistRelease, got %d", list.Total)
	}
	cleared, err := m.ClearBlocklist(ctx, &autov1.ClearBlocklistRequest{ClearAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Removed != 2 {
		t.Fatalf("expected removed=2, got %d", cleared.Removed)
	}
}

func TestUpdateQueueItemByLibraryID(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	if _, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType: "movie", ItemId: "mv1", Title: "Dune", Year: 2021, QualityProfileId: "qp_hd",
	}); err != nil {
		t.Fatal(err)
	}
	up, err := m.UpdateQueueItem(ctx, &autov1.UpdateQueueItemRequest{
		QueueId: "mv1", QualityProfileId: "qp_uhd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Item.GetQualityProfileId() != "qp_uhd" || up.Item.GetItemId() != "mv1" {
		t.Fatalf("%+v", up.Item)
	}
	q, err := m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil || len(q.Items) != 1 || q.Items[0].QualityProfileId != "qp_uhd" {
		t.Fatalf("queue after update: %+v err=%v", q, err)
	}
}

func TestUpdateQueueItemSeriesProfile(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	if _, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType: "tv", ItemId: "e1", SeriesId: "s1", Title: "Orbital S01E01", Year: 2024,
		SeasonNumber: 1, EpisodeNumber: 1, QualityProfileId: "qp_hd",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType: "tv", ItemId: "e2", SeriesId: "s1", Title: "Orbital S01E02", Year: 2024,
		SeasonNumber: 1, EpisodeNumber: 2, QualityProfileId: "qp_hd",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateQueueItem(ctx, &autov1.UpdateQueueItemRequest{
		QueueId: "s1", QualityProfileId: "qp_uhd",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := m.GetQueue(ctx, &autov1.GetQueueRequest{PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Items) != 2 {
		t.Fatalf("items=%d", len(q.Items))
	}
	for _, it := range q.Items {
		if it.QualityProfileId != "qp_uhd" {
			t.Fatalf("episode %s still %q", it.ItemId, it.QualityProfileId)
		}
	}
}

func TestDelayProfileCRUD(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	list, err := m.ListDelayProfiles(ctx, &autov1.ListDelayProfilesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Profiles) < 1 {
		t.Fatal("expected seeded delay profiles")
	}
	up, err := m.UpsertDelayProfile(ctx, &autov1.UpsertDelayProfileRequest{
		Protocol: "torrent", WaitMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Profile.WaitMinutes != 30 {
		t.Fatalf("got %d", up.Profile.WaitMinutes)
	}
	if m.delayMinutesFor(ctx, "torrent") != 30 {
		t.Fatal("delay not applied")
	}
}
