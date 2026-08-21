package internal

import (
	"strings"
	"testing"

	"github.com/Muxcore-Media/core/pkg/contracts"
)

func TestParseMagnetIdentityV1Hex(t *testing.T) {
	id := parseMagnetIdentity("magnet:?xt=urn:btih:A7A087E4C0431DB6E50D8651E02C243CF5B22DCD&dn=Dune.2021")
	if id.InfoHash != "a7a087e4c0431db6e50d8651e02c243cf5b22dcd" {
		t.Fatalf("infohash: %q", id.InfoHash)
	}
	if id.Display != "Dune.2021" {
		t.Fatalf("dn: %q", id.Display)
	}
	if id.InfoHashV2 != "" {
		t.Fatalf("unexpected v2 %q", id.InfoHashV2)
	}
}

func TestParseMagnetIdentityV1Base32(t *testing.T) {
	// 20 zero bytes in base32
	id := parseMagnetIdentity("magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if id.InfoHash != "0000000000000000000000000000000000000000" {
		t.Fatalf("base32 btih: %q", id.InfoHash)
	}
}

func TestParseMagnetIdentityV2AndHybrid(t *testing.T) {
	v2only := parseMagnetIdentity("magnet:?xt=urn:btmh:1220caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e&dn=v2")
	if v2only.InfoHashV2 != "caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e" {
		t.Fatalf("v2: %q", v2only.InfoHashV2)
	}
	hybrid := parseMagnetIdentity("magnet:?xt=urn:btih:631a31dd0a46257d5078c0dee4e66e26f73e42ac&xt=urn:btmh:1220d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb&dn=hyb")
	if hybrid.InfoHash != "631a31dd0a46257d5078c0dee4e66e26f73e42ac" {
		t.Fatalf("hybrid v1: %q", hybrid.InfoHash)
	}
	if hybrid.InfoHashV2 != "d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb" {
		t.Fatalf("hybrid v2: %q", hybrid.InfoHashV2)
	}
}

func TestParseMagnetIdentityHTTP(t *testing.T) {
	id := parseMagnetIdentity("http://127.0.0.1:9696/2/download?apikey=x")
	if id.InfoHash != "" || id.InfoHashV2 != "" {
		t.Fatalf("http should have no hash: %+v", id)
	}
}

func TestMergeMagnetsUnionsTrackers(t *testing.T) {
	a := "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&dn=Same&tr=udp://a.example:80/announce"
	c := "magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&dn=Same&tr=udp://c.example:80/announce&tr=udp://a.example:80/announce"
	got := mergeMagnets([]string{a, c})
	if !strings.Contains(got, "urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatalf("missing btih: %s", got)
	}
	if !strings.Contains(got, "udp://a.example:80/announce") || !strings.Contains(got, "udp://c.example:80/announce") {
		t.Fatalf("missing trackers: %s", got)
	}
	if strings.Count(strings.ToLower(got), "udp://a.example:80/announce") != 1 {
		t.Fatalf("duplicate tracker: %s", got)
	}
}

func TestMergeMagnetsHybridKeepsBothHashes(t *testing.T) {
	a := "magnet:?xt=urn:btih:631a31dd0a46257d5078c0dee4e66e26f73e42ac"
	c := "magnet:?xt=urn:btmh:1220d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb"
	got := mergeMagnets([]string{a, c})
	if !strings.Contains(got, "urn:btih:631a31dd0a46257d5078c0dee4e66e26f73e42ac") {
		t.Fatalf("missing v1: %s", got)
	}
	if !strings.Contains(got, "urn:btmh:1220d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb") {
		t.Fatalf("missing v2: %s", got)
	}
}

func TestFilesFingerprintSortsSizes(t *testing.T) {
	got := filesFingerprint([]contracts.DownloadEventFile{
		{Path: "b", Size: 20},
		{Path: "a", Size: 10},
	})
	if got != "10:20" {
		t.Fatalf("got %q", got)
	}
}

func TestPartialSavePath(t *testing.T) {
	got := partialSavePath("mv_550", "btih_abc")
	if got != "partials/mv_550/btih_abc" {
		t.Fatalf("got %q", got)
	}
	if partialDirIdentity(magnetIdentity{InfoHash: "deadbeef"}) != "btih_deadbeef" {
		t.Fatal("v1 identity")
	}
	if partialDirIdentity(magnetIdentity{InfoHashV2: "cafe"}) != "btmh_cafe" {
		t.Fatal("v2 identity")
	}
}

func TestCanonicalPartialSavePathMeshPending(t *testing.T) {
	ih := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	got := canonicalPartialSavePath("storage://torrent/pending", ih)
	if got != "storage://torrent/"+ih {
		t.Fatalf("got %q", got)
	}
	if canonicalPartialSavePath("storage://torrent/"+ih, ih) != "storage://torrent/"+ih {
		t.Fatal("already hashed path unchanged")
	}
}

func TestAbsoluteDownloadPathJoinsRoot(t *testing.T) {
	m := &Module{downloadDir: "/data/downloads"}
	got := m.absoluteDownloadPath("partials/mv_550/btih_abc")
	if got != "/data/downloads/partials/mv_550/btih_abc" {
		t.Fatalf("got %q", got)
	}
	if m.absoluteDownloadPath("/abs/already") != "/abs/already" {
		t.Fatal("abs passthrough")
	}
	empty := &Module{}
	if empty.absoluteDownloadPath("partials/x") != "partials/x" {
		t.Fatal("no root keeps relative")
	}
}
