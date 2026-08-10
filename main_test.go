package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// === Existing domain/adapter tests ===

func TestParseLibraryItemsJSON(t *testing.T) {
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

	items, err := parseLibraryItemsJSON(&osFS{}, path)
	if err != nil {
		t.Fatalf("parseLibraryItemsJSON returned error: %v", err)
	}

	want := []Book{{ASIN: "B001"}, {ASIN: "B002"}}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("unexpected items: got %v want %v", items, want)
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
	cli := newAudibleCLI("audible", "")
	got := cli.args("library", "list")
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
		"series/Sub_Book.m4b",
		"series/Sub_Needs.aax",
	}
	for _, name := range files {
		path := filepath.Join(mediaDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create dir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("create media file %s: %v", name, err)
		}
	}

	app := &App{FS: &osFS{}, MediaDir: mediaDir}
	state, err := app.scanMediaState()
	if err != nil {
		t.Fatalf("scanMediaState returned error: %v", err)
	}

	if !reflect.DeepEqual(state.Ready, []string{"Ready_Book", "series/Sub_Book"}) {
		t.Fatalf("unexpected ready list: %v", state.Ready)
	}
	if !reflect.DeepEqual(state.NeedsAAX, []string{"Needs_Conversion", "series/Sub_Needs"}) {
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

	app := &App{FS: &osFS{}}
	key, iv, err := app.loadVoucherKeyIV(voucherPath)
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
		info    MediaInfo
		want    string
	}{
		{name: "ready", tracked: true, info: MediaInfo{HasM4B: true}, want: "ready"},
		{name: "needs aax", tracked: true, info: MediaInfo{HasAAX: true}, want: "needs_convert_aax"},
		{name: "needs aaxc", tracked: true, info: MediaInfo{HasAAXC: true}, want: "needs_convert_aaxc"},
		{name: "tracked no media", tracked: true, info: MediaInfo{}, want: "tracked_no_media"},
		{name: "not downloaded", tracked: false, info: MediaInfo{}, want: "not_downloaded"},
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

func TestSanitizeFileName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "Hello World"},
		{"Book: Title", "Book- Title"},
		{"A/B Testing", "A-B Testing"},
		{"File*Name?", "FileName"},
		{"  Multiple   Spaces  ", "Multiple Spaces"},
		{"..", "_"},
		{".", "_"},
		{"", "_"},
	}

	for _, tt := range tests {
		got := sanitizeFileName(tt.input)
		if got != tt.want {
			t.Fatalf("sanitizeFileName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatPrefix(t *testing.T) {
	tests := []struct {
		seq  interface{}
		want string
	}{
		{nil, ""},
		{float64(1), "01 - "},
		{float64(15), "15 - "},
		{"3", "03 - "},
		{float64(0), ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := formatPrefix(tt.seq)
		if got != tt.want {
			t.Fatalf("formatPrefix(%v) = %q, want %q", tt.seq, got, tt.want)
		}
	}
}

func TestRenameDownloadedFiles(t *testing.T) {
	dir := t.TempDir()

	files := []string{
		"B001.aax",
		"B001.voucher",
		"B001-chapters.json",
		"B001.jpg",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("create file %s: %v", name, err)
		}
	}

	app := &App{FS: &osFS{}}
	if err := app.renameDownloadedFiles(dir, "B001", "My Book: Title", false, nil); err != nil {
		t.Fatalf("renameDownloadedFiles failed: %v", err)
	}

	expected := []string{
		"My Book- Title.aax",
		"My Book- Title.voucher",
		"My Book- Title-chapters.json",
		"My Book- Title.jpg",
	}
	for _, name := range expected {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %s to exist: %v", name, err)
		}
	}
}

func TestRenameDownloadedFilesStripsAudibleQualitySuffix(t *testing.T) {
	dir := t.TempDir()

	files := []string{
		"B003-LC_128_44100_stereo.aaxc",
		"B003-LC_128_44100_stereo.voucher",
		"B003-LC_128_44100_stereo-chapters.json",
		"B003-AAX_44_128.aax",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("create file %s: %v", name, err)
		}
	}

	app := &App{FS: &osFS{}}
	if err := app.renameDownloadedFiles(dir, "B003", "The Book of Joy", false, nil); err != nil {
		t.Fatalf("renameDownloadedFiles failed: %v", err)
	}

	expected := []string{
		"The Book of Joy.aaxc",
		"The Book of Joy.voucher",
		"The Book of Joy-chapters.json",
		"The Book of Joy.aax",
	}
	for _, name := range expected {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %s to exist: %v", name, err)
		}
	}
}

func TestConversionOutputPathStripsAudibleQualitySuffix(t *testing.T) {
	got := conversionOutputPath(filepath.Join("media", "The Book of Joy-LC_128_44100_stereo.aaxc"))
	want := filepath.Join("media", "The Book of Joy.m4b")
	if got != want {
		t.Fatalf("conversionOutputPath() = %q, want %q", got, want)
	}
}

func TestRenameDownloadedFilesWithSeries(t *testing.T) {
	dir := t.TempDir()

	files := []string{
		"B002.aaxc",
		"B002.voucher",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("create file %s: %v", name, err)
		}
	}

	app := &App{FS: &osFS{}}
	if err := app.renameDownloadedFiles(dir, "B002", "Armor World", true, float64(15)); err != nil {
		t.Fatalf("renameDownloadedFiles failed: %v", err)
	}

	expected := []string{
		"15 - Armor World.aaxc",
		"15 - Armor World.voucher",
	}
	for _, name := range expected {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %s to exist: %v", name, err)
		}
	}
}

func TestRenameDownloadedFilesLeavesSidecarsTogetherOnCollision(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"B004.aaxc", "B004.voucher", "Existing Book.aaxc"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("create file %s: %v", name, err)
		}
	}

	app := &App{FS: &osFS{}}
	if err := app.renameDownloadedFiles(dir, "B004", "Existing Book", false, nil); err == nil {
		t.Fatal("expected target collision")
	}
	for _, name := range []string{"B004.aaxc", "B004.voucher"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected source %s to remain: %v", name, err)
		}
	}
}

// === New CLI tests ===

func TestRun_HelpCommand(t *testing.T) {
	if err := run(context.Background(), []string{"help"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_HelpSpecificCommand(t *testing.T) {
	if err := run(context.Background(), []string{"help", "status"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	err := run(context.Background(), []string{"unknown"})
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestRun_NoCommand(t *testing.T) {
	err := run(context.Background(), []string{})
	if err == nil {
		t.Fatal("expected error for no command")
	}
}

func TestRun_StatusTableFlag(t *testing.T) {
	cmd := buildCommands()["status"]
	fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
	cmd.Setup(fs)
	if err := fs.Parse([]string{"-table"}); err != nil {
		t.Fatalf("parse status flags: %v", err)
	}
	if fs.Lookup("table").Value.String() != "true" {
		t.Fatal("expected -table to be true")
	}
}
