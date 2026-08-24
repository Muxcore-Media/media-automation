package internal

import "testing"

func TestHistoryStatusLabel(t *testing.T) {
	tests := []struct {
		status, detail, want string
	}{
		{"sent", "", "Downloading"},
		{"completed", "", "Completed"},
		{"import_failed", "path not under a scanner watch directory", "Import failed: path not under a scanner watch directory"},
		{"failed", "download failed", "Download failed: download failed"},
		{"stalled", "stalled: no progress for 1h0m0s", "Stalled: stalled: no progress for 1h0m0s"},
		{"pending", "", "Pending"},
		{"custom_state", "note", "Custom state: note"},
	}
	for _, tc := range tests {
		got := HistoryStatusLabel(tc.status, tc.detail)
		if got != tc.want {
			t.Errorf("HistoryStatusLabel(%q, %q) = %q want %q", tc.status, tc.detail, got, tc.want)
		}
	}
}

func TestClassifyImportError(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "import failed"},
		{"path not under any registered watch directory", "path not under a scanner watch directory"},
		{"scanner unavailable: dial timeout", "scanner unavailable"},
		{"context deadline exceeded", "import timed out"},
		{"import path \"/x\": no such file or directory", "download path missing on disk"},
		{"imported 0 files", "no importable files found"},
	}
	for _, tc := range tests {
		got := classifyImportError(tc.in)
		if got != tc.want {
			t.Errorf("classifyImportError(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}
