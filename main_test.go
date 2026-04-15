package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseLibraryJSON(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "library.json")
	contents := `[
		{"asin":"B001"},
		{"asin":" B002 "},
		{"asin":"B001"},
		{"asin":""},
		{}
	]`

	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write temp library: %v", err)
	}

	asins, err := parseLibraryJSON(path)
	if err != nil {
		t.Fatalf("parseLibraryJSON returned error: %v", err)
	}

	want := []string{"B001", "B002"}
	if !reflect.DeepEqual(asins, want) {
		t.Fatalf("unexpected asins: got %v want %v", asins, want)
	}
}

func TestExtractActivationBytes(t *testing.T) {
	output := "Fetching activation bytes from Audible server\nSave activation bytes to file\na196c606\n"

	got := extractActivationBytes(output)
	if got != "a196c606" {
		t.Fatalf("unexpected activation bytes: got %q", got)
	}
}

func TestAudibleArgsSkipsEmptyPassword(t *testing.T) {
	cfg := config{Profile: "audible"}
	got := audibleArgs(cfg, "library", "list")
	want := []string{"--profile", "audible", "library", "list"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args: got %v want %v", got, want)
	}
}

func TestScanMediaState(t *testing.T) {
	mediaDir := t.TempDir()
	files := []string{
		"Ready_Book.m4b",
		"Needs_Conversion.aax",
		"Locked_Title.aaxc",
		"Ready_Book.aax",
		"Ready_Book.aaxc",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(mediaDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("create media file %s: %v", name, err)
		}
	}

	state, err := scanMediaState(mediaDir)
	if err != nil {
		t.Fatalf("scanMediaState returned error: %v", err)
	}

	if !reflect.DeepEqual(state.Ready, []string{"Ready_Book"}) {
		t.Fatalf("unexpected ready list: %v", state.Ready)
	}
	if !reflect.DeepEqual(state.NeedsAAX, []string{"Needs_Conversion"}) {
		t.Fatalf("unexpected needs-aax list: %v", state.NeedsAAX)
	}
	if !reflect.DeepEqual(state.NeedsAAXC, []string{"Locked_Title"}) {
		t.Fatalf("unexpected needs-aaxc list: %v", state.NeedsAAXC)
	}
}

func TestLoadVoucherKeyIV(t *testing.T) {
	voucherPath := filepath.Join(t.TempDir(), "book.voucher")
	content := `{
	  "content_license": {
	    "license_response": {
	      "key": "00112233445566778899aabbccddeeff",
	      "iv": "ffeeddccbbaa99887766554433221100"
	    }
	  }
	}`

	if err := os.WriteFile(voucherPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write voucher: %v", err)
	}

	key, iv, err := loadVoucherKeyIV(voucherPath)
	if err != nil {
		t.Fatalf("loadVoucherKeyIV returned error: %v", err)
	}
	if key != "00112233445566778899aabbccddeeff" {
		t.Fatalf("unexpected key: %s", key)
	}
	if iv != "ffeeddccbbaa99887766554433221100" {
		t.Fatalf("unexpected iv: %s", iv)
	}
}

func TestComputeBookState(t *testing.T) {
	tests := []struct {
		name    string
		tracked bool
		info    bookMediaInfo
		want    string
	}{
		{name: "ready", tracked: true, info: bookMediaInfo{HasM4B: true}, want: "ready"},
		{name: "needs aax", tracked: true, info: bookMediaInfo{HasAAX: true}, want: "needs_convert_aax"},
		{name: "needs aaxc", tracked: true, info: bookMediaInfo{HasAAXC: true}, want: "needs_convert_aaxc"},
		{name: "tracked no media", tracked: true, info: bookMediaInfo{}, want: "tracked_no_media"},
		{name: "not downloaded", tracked: false, info: bookMediaInfo{}, want: "not_downloaded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeBookState(tt.tracked, tt.info)
			if got != tt.want {
				t.Fatalf("unexpected state: got %q want %q", got, tt.want)
			}
		})
	}
}
