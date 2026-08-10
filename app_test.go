package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// fakeAudible is a test double for AudibleService.
type fakeAudible struct {
	hasProfile         bool
	hasProfileErr      error
	quickstartErr      error
	library            []Book
	libraryErr         error
	downloaded         []string
	downloadErr        error
	activationBytes    string
	activationBytesErr error
	audioParts         map[string][]string
	audioPartsErr      error
	audioPartsCalls    []string
}

func (f *fakeAudible) HasProfile(ctx context.Context) (bool, error) {
	return f.hasProfile, f.hasProfileErr
}
func (f *fakeAudible) RunQuickstart(ctx context.Context) error {
	if f.quickstartErr != nil {
		return f.quickstartErr
	}
	f.hasProfile = true
	return nil
}
func (f *fakeAudible) ExportLibrary(ctx context.Context) ([]Book, error) {
	return f.library, f.libraryErr
}
func (f *fakeAudible) DownloadBook(ctx context.Context, asin, outputDir string) error {
	if f.downloadErr != nil {
		return f.downloadErr
	}
	f.downloaded = append(f.downloaded, asin)
	return nil
}
func (f *fakeAudible) GetActivationBytes(ctx context.Context) (string, error) {
	return f.activationBytes, f.activationBytesErr
}
func (f *fakeAudible) GetAudioParts(ctx context.Context, asin string) ([]string, error) {
	f.audioPartsCalls = append(f.audioPartsCalls, asin)
	if f.audioPartsErr != nil {
		return nil, f.audioPartsErr
	}
	return f.audioParts[asin], nil
}

// fakeConverter is a test double for MediaConverter.
type fakeConverter struct {
	aaxCalls    []struct{ input, output, bytes string }
	aaxcCalls   []struct{ input, output, key, iv string }
	concatCalls []struct{ listPath, output string }
	aaxErr      error
	aaxcErr     error
	concatErr   error
	fs          *fakeFS
	writeOutput bool
}

func (f *fakeConverter) ConvertAAX(ctx context.Context, inputPath, outputPath, activationBytes string) error {
	f.aaxCalls = append(f.aaxCalls, struct{ input, output, bytes string }{inputPath, outputPath, activationBytes})
	if f.writeOutput {
		f.fs.files[outputPath] = []byte("partial")
	}
	return f.aaxErr
}
func (f *fakeConverter) ConvertAAXC(ctx context.Context, inputPath, outputPath, key, iv string) error {
	f.aaxcCalls = append(f.aaxcCalls, struct{ input, output, key, iv string }{inputPath, outputPath, key, iv})
	return f.aaxcErr
}
func (f *fakeConverter) ConcatM4B(ctx context.Context, listPath, outputPath string) error {
	f.concatCalls = append(f.concatCalls, struct{ listPath, output string }{listPath, outputPath})
	return f.concatErr
}

// fakeFS is an in-memory FileSystem for tests.
type fakeFS struct {
	files   map[string][]byte
	dirs    map[string]bool
	entries map[string][]os.DirEntry
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		files:   make(map[string][]byte),
		dirs:    make(map[string]bool),
		entries: make(map[string][]os.DirEntry),
	}
}

func (f *fakeFS) MkdirAll(path string, perm os.FileMode) error {
	f.dirs[path] = true
	return nil
}
func (f *fakeFS) ReadDir(name string) ([]os.DirEntry, error) {
	entries, ok := f.entries[name]
	if !ok {
		return nil, fmt.Errorf("no such file or directory")
	}
	return entries, nil
}
func (f *fakeFS) ReadFile(name string) ([]byte, error) {
	data, ok := f.files[name]
	if !ok {
		return nil, fmt.Errorf("no such file or directory")
	}
	return data, nil
}
func (f *fakeFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	f.files[name] = data
	return nil
}
func (f *fakeFS) Rename(oldpath, newpath string) error {
	data, ok := f.files[oldpath]
	if !ok {
		return fmt.Errorf("no such file or directory")
	}
	delete(f.files, oldpath)
	f.files[newpath] = data
	return nil
}
func (f *fakeFS) Remove(name string) error {
	delete(f.files, name)
	return nil
}
func (f *fakeFS) Stat(name string) (os.FileInfo, error) {
	if _, ok := f.dirs[name]; ok {
		return &fakeFileInfo{name: filepath.Base(name), isDir: true}, nil
	}
	if _, ok := f.files[name]; ok {
		return &fakeFileInfo{name: filepath.Base(name)}, nil
	}
	return nil, os.ErrNotExist
}
func (f *fakeFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	for path := range f.files {
		if !hasPrefix(path, root) {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." || rel == "" {
			continue
		}
		name := filepath.Base(path)
		d := &fakeDirEntry{name: name, isDir: false}
		if err := fn(path, d, nil); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeFS) CreateTemp(dir, pattern string) (*os.File, error) {
	return nil, errors.New("not implemented")
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

type fakeFileInfo struct {
	name  string
	isDir bool
}

func (f *fakeFileInfo) Name() string       { return f.name }
func (f *fakeFileInfo) Size() int64        { return 0 }
func (f *fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f *fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f *fakeFileInfo) IsDir() bool        { return f.isDir }
func (f *fakeFileInfo) Sys() interface{}   { return nil }

type fakeDirEntry struct {
	name  string
	isDir bool
}

func (d *fakeDirEntry) Name() string      { return d.name }
func (d *fakeDirEntry) IsDir() bool       { return d.isDir }
func (d *fakeDirEntry) Type() os.FileMode { return 0 }
func (d *fakeDirEntry) Info() (os.FileInfo, error) {
	return &fakeFileInfo{name: d.name, isDir: d.isDir}, nil
}

// fakeStore is a test double for ASINStore.
type fakeStore struct {
	asins   []string
	saveErr error
}

func (f *fakeStore) Load() ([]string, error) {
	return f.asins, nil
}
func (f *fakeStore) Save(asins []string) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.asins = asins
	return nil
}

// fakePrompter is a test double for Prompter.
type fakePrompter struct {
	answers []bool
	idx     int
	err     error
}

func (f *fakePrompter) PromptYesNo(prompt string, defaultYes bool) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.idx >= len(f.answers) {
		return false, errors.New("no more answers")
	}
	ans := f.answers[f.idx]
	f.idx++
	return ans, nil
}

func TestDownload_SkipsAlreadyDownloaded(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}

	store := &fakeStore{asins: []string{"B001"}}
	audible := &fakeAudible{
		library: []Book{
			{ASIN: "B001", Title: "First Book"},
			{ASIN: "B002", Title: "Second Book"},
		},
	}
	app := &App{
		Audible:         audible,
		FS:              fs,
		Store:           store,
		MediaDir:        "media",
		DownloadWorkers: 1,
	}

	if err := app.Download(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(audible.downloaded) != 1 {
		t.Fatalf("expected 1 download, got %d", len(audible.downloaded))
	}
	if audible.downloaded[0] != "B002" {
		t.Fatalf("expected B002, got %s", audible.downloaded[0])
	}
}

func TestDownload_CreatesSeriesDirectory(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}

	store := &fakeStore{}
	audible := &fakeAudible{
		library: []Book{
			{ASIN: "B003", Title: "Series Book", SeriesTitle: "My Series", SeriesSequence: float64(2)},
		},
	}
	app := &App{
		Audible:         audible,
		FS:              fs,
		Store:           store,
		MediaDir:        "media",
		DownloadWorkers: 1,
	}

	if err := app.Download(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fs.dirs["media/My Series"] {
		t.Fatal("expected series directory to be created")
	}
}

func TestDownload_WritesPartsManifestForMultiPartBook(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}

	store := &fakeStore{}
	audible := &fakeAudible{
		library: []Book{
			{ASIN: "B0DK2815FK", Title: "This Inevitable Ruin"},
		},
		audioParts: map[string][]string{
			"B0DK2815FK": {"B0DWPNQK52", "B0DWP16T86", "B0DWP7S13J"},
		},
	}
	app := &App{
		Audible:         audible,
		FS:              fs,
		Store:           store,
		MediaDir:        "media",
		DownloadWorkers: 1,
	}

	if err := app.Download(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, ok := fs.files["media/B0DK2815FK-parts.json"]
	if !ok {
		t.Fatal("expected parts manifest to be written")
	}

	var manifest partsManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}
	if manifest.ASIN != "B0DK2815FK" {
		t.Fatalf("unexpected asin: %s", manifest.ASIN)
	}
	if manifest.Title != "This Inevitable Ruin" {
		t.Fatalf("unexpected title: %s", manifest.Title)
	}
	want := []string{"B0DWPNQK52", "B0DWP16T86", "B0DWP7S13J"}
	if !reflect.DeepEqual(manifest.Parts, want) {
		t.Fatalf("unexpected parts: got %v want %v", manifest.Parts, want)
	}
	if manifest.OutputBase != "This Inevitable Ruin" {
		t.Fatalf("unexpected output base: %s", manifest.OutputBase)
	}
}

func TestDownload_NoManifestForSinglePartBook(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}

	store := &fakeStore{}
	audible := &fakeAudible{
		library: []Book{
			{ASIN: "B001", Title: "Normal Book"},
		},
	}
	app := &App{
		Audible:         audible,
		FS:              fs,
		Store:           store,
		MediaDir:        "media",
		DownloadWorkers: 1,
	}

	if err := app.Download(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := fs.files["media/B001-parts.json"]; ok {
		t.Fatal("did not expect a parts manifest for a single-part book")
	}
}

func TestDownload_ReturnsDownloadErrors(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}

	store := &fakeStore{}
	audible := &fakeAudible{
		library: []Book{
			{ASIN: "B004", Title: "Good Book"},
			{ASIN: "B005", Title: "Bad Book"},
		},
		downloadErr: errors.New("network error"),
	}
	app := &App{
		Audible:         audible,
		FS:              fs,
		Store:           store,
		MediaDir:        "media",
		DownloadWorkers: 1,
	}

	if err := app.Download(context.Background()); err == nil {
		t.Fatal("expected download failures to be returned")
	}

	if len(store.asins) != 0 {
		t.Fatalf("expected no ASINs saved after all failures, got %v", store.asins)
	}
}

func TestDownload_ReturnsStoreErrors(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}

	app := &App{
		Audible:         &fakeAudible{library: []Book{{ASIN: "B001", Title: "Book"}}},
		FS:              fs,
		Store:           &fakeStore{saveErr: errors.New("disk full")},
		MediaDir:        "media",
		DownloadWorkers: 1,
	}

	if err := app.Download(context.Background()); err == nil {
		t.Fatal("expected store failures to be returned")
	}
}

func TestConvert_CallsConverterWithActivationBytes(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/book1.aax"] = []byte("aax data")
	fs.files["media/book2.aaxc"] = []byte("aaxc data")
	fs.files["media/book2.voucher"] = []byte(`{"content_license":{"license_response":{"key":"key1","iv":"iv1"}}}`)

	converter := &fakeConverter{}
	audible := &fakeAudible{activationBytes: "a1b2c3d4"}
	app := &App{
		Audible:   audible,
		Converter: converter,
		FS:        fs,
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}

	if err := app.Convert(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(converter.aaxCalls) != 1 {
		t.Fatalf("expected 1 aax conversion, got %d", len(converter.aaxCalls))
	}
	if converter.aaxCalls[0].bytes != "a1b2c3d4" {
		t.Fatalf("unexpected activation bytes: %s", converter.aaxCalls[0].bytes)
	}
	if len(converter.aaxcCalls) != 1 {
		t.Fatalf("expected 1 aaxc conversion, got %d", len(converter.aaxcCalls))
	}
	if converter.aaxcCalls[0].key != "key1" {
		t.Fatalf("unexpected key: %s", converter.aaxcCalls[0].key)
	}
}

func TestConvert_SkipsExistingOutput(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/book.aax"] = []byte("aax data")
	fs.files["media/book.m4b"] = []byte("finished")
	converter := &fakeConverter{}
	app := &App{
		Audible:   &fakeAudible{},
		Converter: converter,
		FS:        fs,
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}

	if err := app.Convert(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(converter.aaxCalls) != 0 {
		t.Fatal("expected existing output to be preserved")
	}
}

func TestConvert_RemovesFailedOutput(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/book.aax"] = []byte("aax data")
	converter := &fakeConverter{fs: fs, writeOutput: true, aaxErr: errors.New("conversion failed")}
	app := &App{
		Audible:   &fakeAudible{activationBytes: "a1b2c3d4"},
		Converter: converter,
		FS:        fs,
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}

	if err := app.Convert(context.Background()); err == nil {
		t.Fatal("expected conversion failure")
	}
	if _, ok := fs.files["media/book.m4b"]; ok {
		t.Fatal("expected partial output to be removed")
	}
}

func TestConvert_MergesMultiPartBook(t *testing.T) {
	fs := newFakeFS()
	manifest := partsManifest{
		ASIN:       "B0DK2815FK",
		Title:      "This Inevitable Ruin",
		Parts:      []string{"B0DWPNQK52", "B0DWP16T86", "B0DWP7S13J"},
		OutputBase: "This Inevitable Ruin",
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}
	fs.files["media/B0DK2815FK-parts.json"] = manifestData
	fs.files["media/B0DWPNQK52.m4b"] = []byte("part1")
	fs.files["media/B0DWP16T86.m4b"] = []byte("part2")
	fs.files["media/B0DWP7S13J.m4b"] = []byte("part3")

	converter := &fakeConverter{}
	app := &App{
		Audible:   &fakeAudible{},
		Converter: converter,
		FS:        fs,
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}

	if err := app.Convert(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(converter.concatCalls) != 1 {
		t.Fatalf("expected 1 concat call, got %d", len(converter.concatCalls))
	}
	if converter.concatCalls[0].output != "media/This Inevitable Ruin.m4b" {
		t.Fatalf("unexpected concat output: %s", converter.concatCalls[0].output)
	}

	if _, ok := fs.files["media/B0DWPNQK52.m4b"]; ok {
		t.Fatal("expected part 1 m4b to be removed after merge")
	}
	if _, ok := fs.files["media/B0DWP16T86.m4b"]; ok {
		t.Fatal("expected part 2 m4b to be removed after merge")
	}
	if _, ok := fs.files["media/B0DWP7S13J.m4b"]; ok {
		t.Fatal("expected part 3 m4b to be removed after merge")
	}
	if _, ok := fs.files["media/B0DK2815FK-parts.json"]; ok {
		t.Fatal("expected manifest to be removed after merge")
	}
}

func TestConvert_SkipsMergeWhenPartsIncomplete(t *testing.T) {
	fs := newFakeFS()
	manifest := partsManifest{
		ASIN:       "B0DK2815FK",
		Title:      "This Inevitable Ruin",
		Parts:      []string{"B0DWPNQK52", "B0DWP16T86"},
		OutputBase: "This Inevitable Ruin",
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}
	fs.files["media/B0DK2815FK-parts.json"] = manifestData
	fs.files["media/B0DWPNQK52.m4b"] = []byte("part1")
	// B0DWP16T86.m4b not converted yet.

	converter := &fakeConverter{}
	app := &App{
		Audible:   &fakeAudible{},
		Converter: converter,
		FS:        fs,
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}

	if err := app.Convert(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(converter.concatCalls) != 0 {
		t.Fatalf("expected no concat call, got %d", len(converter.concatCalls))
	}
	if _, ok := fs.files["media/B0DK2815FK-parts.json"]; !ok {
		t.Fatal("expected manifest to be kept for retry")
	}
}

func TestEscapeFFconcatPath(t *testing.T) {
	got := escapeFFconcatPath("/media/Reader's Series/part.m4b")
	want := "/media/Reader'\\''s Series/part.m4b"
	if got != want {
		t.Fatalf("unexpected escaped path: got %q want %q", got, want)
	}
}

func TestConvert_ReturnsErrorWhenFfmpegMissing(t *testing.T) {
	app := &App{
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
	}

	err := app.Convert(context.Background())
	if err == nil {
		t.Fatal("expected error when ffmpeg is missing")
	}
}

func TestClean_RemovesOnlyIntermediateFiles(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/book.aax"] = []byte("x")
	fs.files["media/book.aaxc"] = []byte("x")
	fs.files["media/book.jpg"] = []byte("x")
	fs.files["media/book.json"] = []byte("x")
	fs.files["media/book.voucher"] = []byte("x")
	fs.files["media/book.pdf"] = []byte("x")
	fs.files["media/LOUD.AAX"] = []byte("x")
	fs.files["media/book.m4b"] = []byte("x")
	fs.files["media/book.txt"] = []byte("x")

	app := &App{FS: fs, MediaDir: "media"}
	if err := app.Clean(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := fs.files["media/book.m4b"]; !ok {
		t.Fatal("expected .m4b to be preserved")
	}
	if _, ok := fs.files["media/book.txt"]; !ok {
		t.Fatal("expected .txt to be preserved")
	}
	if _, ok := fs.files["media/book.aax"]; ok {
		t.Fatal("expected .aax to be removed")
	}
	if _, ok := fs.files["media/book.aaxc"]; ok {
		t.Fatal("expected .aaxc to be removed")
	}
	if _, ok := fs.files["media/book.jpg"]; ok {
		t.Fatal("expected .jpg to be removed")
	}
	if _, ok := fs.files["media/book.json"]; ok {
		t.Fatal("expected .json to be removed")
	}
	if _, ok := fs.files["media/book.voucher"]; ok {
		t.Fatal("expected .voucher to be removed")
	}
	if _, ok := fs.files["media/book.pdf"]; ok {
		t.Fatal("expected .pdf to be removed")
	}
	if _, ok := fs.files["media/LOUD.AAX"]; ok {
		t.Fatal("expected uppercase .AAX to be removed")
	}
}

func TestStatus_ReportsCorrectCounts(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/book1.m4b"] = []byte("x")
	fs.files["media/book2.aax"] = []byte("x")
	fs.files["media/book3.aaxc"] = []byte("x")

	store := &fakeStore{asins: []string{"B001", "B002"}}
	audible := &fakeAudible{
		library: []Book{
			{ASIN: "B001", Title: "Book One"},
			{ASIN: "B002", Title: "Book Two"},
			{ASIN: "B003", Title: "Book Three"},
		},
	}
	app := &App{
		Audible:  audible,
		FS:       fs,
		Store:    store,
		MediaDir: "media",
	}

	if err := app.Status(context.Background(), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// We can't easily capture stdout here, but we can test the underlying state
	state, err := app.scanMediaState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state.Ready) != 1 {
		t.Fatalf("expected 1 ready, got %d", len(state.Ready))
	}
	if len(state.NeedsAAX) != 1 {
		t.Fatalf("expected 1 needs aax, got %d", len(state.NeedsAAX))
	}
	if len(state.NeedsAAXC) != 1 {
		t.Fatalf("expected 1 needs aaxc, got %d", len(state.NeedsAAXC))
	}
}

func TestEnsureAudibleConfigured_AlreadyConfigured(t *testing.T) {
	audible := &fakeAudible{hasProfile: true}
	app := &App{
		Audible:     audible,
		Prompter:    &fakePrompter{},
		lookPath:    func(string) (string, error) { return "/usr/bin/audible", nil },
		interactive: func() bool { return true },
	}

	if err := app.EnsureAudibleConfigured(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureAudibleConfigured_ReturnsProfileInspectionError(t *testing.T) {
	wantErr := errors.New("broken audible config")
	app := &App{
		Audible:     &fakeAudible{hasProfileErr: wantErr},
		Prompter:    &fakePrompter{},
		lookPath:    func(string) (string, error) { return "/usr/bin/audible", nil },
		interactive: func() bool { return true },
	}

	err := app.EnsureAudibleConfigured(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected profile error, got %v", err)
	}
}

func TestEnsureAudibleConfigured_PromptsForQuickstart(t *testing.T) {
	audible := &fakeAudible{hasProfile: false}
	app := &App{
		Audible:     audible,
		Prompter:    &fakePrompter{answers: []bool{true}},
		lookPath:    func(string) (string, error) { return "/usr/bin/audible", nil },
		interactive: func() bool { return true },
	}

	if err := app.EnsureAudibleConfigured(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureAudibleConfigured_RejectsQuickstart(t *testing.T) {
	audible := &fakeAudible{hasProfile: false}
	app := &App{
		Audible:     audible,
		Prompter:    &fakePrompter{answers: []bool{false}},
		lookPath:    func(string) (string, error) { return "/usr/bin/audible", nil },
		interactive: func() bool { return true },
	}

	err := app.EnsureAudibleConfigured(context.Background())
	if err == nil {
		t.Fatal("expected error when user rejects quickstart")
	}
}

func TestBuildASINMediaIndex(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/B001.voucher"] = []byte(`{"content_license":{"asin":"B001","content_metadata":{"content_reference":{"content_format":"aaxc"}},"license_response":{"key":"k","iv":"i"}}}`)
	fs.files["media/B001.aaxc"] = []byte("x")
	fs.files["media/B001.m4b"] = []byte("x")

	app := &App{FS: fs, MediaDir: "media"}
	index, err := app.buildASINMediaIndex()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, ok := index["B001"]
	if !ok {
		t.Fatal("expected B001 in index")
	}
	if !info.HasM4B || !info.HasAAXC {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestFindFilesRecursiveFS(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/a.aax"] = []byte("x")
	fs.files["media/sub/b.aax"] = []byte("x")
	fs.files["media/c.m4b"] = []byte("x")

	files, err := findFilesRecursiveFS(fs, "media", ".aax")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sort.Strings(files)
	want := []string{"media/a.aax", "media/sub/b.aax"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("unexpected files: got %v want %v", files, want)
	}
}
