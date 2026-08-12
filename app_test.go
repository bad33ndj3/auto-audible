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
	if f.fs != nil && (f.writeOutput || f.aaxErr == nil) {
		f.fs.files[outputPath] = []byte("partial")
	}
	return f.aaxErr
}
func (f *fakeConverter) ConvertAAXC(ctx context.Context, inputPath, outputPath, key, iv string) error {
	f.aaxcCalls = append(f.aaxcCalls, struct{ input, output, key, iv string }{inputPath, outputPath, key, iv})
	if f.fs != nil && (f.writeOutput || f.aaxcErr == nil) {
		f.fs.files[outputPath] = []byte("partial")
	}
	return f.aaxcErr
}
func (f *fakeConverter) ConcatM4B(ctx context.Context, listPath, outputPath string) error {
	f.concatCalls = append(f.concatCalls, struct{ listPath, output string }{listPath, outputPath})
	if f.fs != nil && (f.writeOutput || f.concatErr == nil) {
		f.fs.files[outputPath] = []byte("merged")
	}
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
	entries, exists := f.entries[name]
	if !exists {
		for path := range f.files {
			if filepath.Dir(path) == name {
				exists = true
				break
			}
		}
	}
	if !exists && !f.dirs[name] {
		return nil, fmt.Errorf("no such file or directory")
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		seen[entry.Name()] = struct{}{}
	}
	for path := range f.files {
		if filepath.Dir(path) != name {
			continue
		}
		file := filepath.Base(path)
		if _, ok := seen[file]; !ok {
			entries = append(entries, &fakeDirEntry{name: file})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
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
	if data, ok := f.files[name]; ok {
		return &fakeFileInfo{name: filepath.Base(name), size: int64(len(data))}, nil
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
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

type fakeFileInfo struct {
	name  string
	isDir bool
	size  int64
}

func (f *fakeFileInfo) Name() string       { return f.name }
func (f *fakeFileInfo) Size() int64        { return f.size }
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
	fs.files["media/First Book.m4b"] = []byte("ready")

	store := &fakeStore{asins: []string{"B001"}}
	audible := &fakeAudible{
		library: []Book{
			{ASIN: "B001", Title: "First Book"},
			{ASIN: "B002", Title: "Second Book"},
		},
	}
	app := &App{
		Audible:  audible,
		FS:       fs,
		Store:    store,
		MediaDir: "media",
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

func TestDownloadPlanShowsWhatSyncWillSkipAndDownload(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/Present Book.m4b"] = []byte("ready")
	app := &App{
		Audible:  &fakeAudible{library: []Book{{ASIN: "B001", Title: "Present Book"}, {ASIN: "B002", Title: "New Book"}}},
		FS:       fs,
		Store:    &fakeStore{asins: []string{"B001"}},
		MediaDir: "media",
	}

	plan, err := app.DownloadPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Present, []Book{{ASIN: "B001", Title: "Present Book"}}) {
		t.Fatalf("present = %v", plan.Present)
	}
	if !reflect.DeepEqual(plan.Download, []Book{{ASIN: "B002", Title: "New Book"}}) {
		t.Fatalf("download = %v", plan.Download)
	}
}

func TestDownloadPlanSkipsOffloadedBookWithoutMedia(t *testing.T) {
	app := &App{
		Audible:   &fakeAudible{library: []Book{{ASIN: "B001", Title: "Offloaded Book"}, {ASIN: "B002", Title: "New Book"}}},
		FS:        newFakeFS(),
		Store:     &fakeStore{asins: []string{"B001"}},
		Offloaded: &fakeStore{asins: []string{"B001"}},
		MediaDir:  "media",
	}

	plan, err := app.DownloadPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Offloaded, []Book{{ASIN: "B001", Title: "Offloaded Book"}}) {
		t.Fatalf("offloaded = %v", plan.Offloaded)
	}
	if !reflect.DeepEqual(plan.Download, []Book{{ASIN: "B002", Title: "New Book"}}) {
		t.Fatalf("download = %v", plan.Download)
	}
}

func TestOffloadPreviewDoesNotChangeMediaOrState(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/Series/01 - Book.m4b"] = []byte("ready")
	offloaded := &fakeStore{}
	app := &App{
		Audible:   &fakeAudible{library: []Book{{ASIN: "B001", Title: "Book", SeriesTitle: "Series", SeriesSequence: "1"}}},
		FS:        fs,
		Offloaded: offloaded,
		MediaDir:  "media",
	}

	if err := app.Offload(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.files["media/Series/01 - Book.m4b"]; !ok {
		t.Fatal("preview removed media")
	}
	if len(offloaded.asins) != 0 {
		t.Fatalf("preview recorded offloaded ASINs: %v", offloaded.asins)
	}
}

func TestOffloadMarksThenRemovesAndPreventsRedownload(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/Book.m4b"] = []byte("ready")
	offloaded := &fakeStore{}
	app := &App{
		Audible:   &fakeAudible{library: []Book{{ASIN: "B001", Title: "Book"}}},
		FS:        fs,
		Store:     &fakeStore{asins: []string{"B001"}},
		Offloaded: offloaded,
		MediaDir:  "media",
	}

	if err := app.Offload(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.files["media/Book.m4b"]; ok {
		t.Fatal("offload did not remove media")
	}
	if !reflect.DeepEqual(offloaded.asins, []string{"B001"}) {
		t.Fatalf("offloaded ASINs = %v", offloaded.asins)
	}
	plan, err := app.DownloadPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Offloaded) != 1 || len(plan.Download) != 0 {
		t.Fatalf("unexpected download plan: %+v", plan)
	}
}

func TestOffloadFailsWithoutChangingAnythingWhenMediaCannotBeMapped(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/Manual.m4b"] = []byte("ready")
	offloaded := &fakeStore{}
	app := &App{
		Audible:   &fakeAudible{library: []Book{{ASIN: "B001", Title: "Book"}}},
		FS:        fs,
		Offloaded: offloaded,
		MediaDir:  "media",
	}

	if err := app.Offload(context.Background(), true); err == nil {
		t.Fatal("expected unmapped media to stop offload")
	}
	if _, ok := fs.files["media/Manual.m4b"]; !ok {
		t.Fatal("unmapped media was removed")
	}
	if len(offloaded.asins) != 0 {
		t.Fatalf("unmapped media was recorded: %v", offloaded.asins)
	}
}

func TestDownload_RetriesTrackedBookWhenMediaIsMissing(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}
	store := &fakeStore{asins: []string{"B001"}}
	audible := &fakeAudible{library: []Book{{ASIN: "B001", Title: "Missing Book"}}}
	app := &App{Audible: audible, FS: fs, Store: store, MediaDir: "media"}

	if err := app.Download(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(audible.downloaded, []string{"B001"}) {
		t.Fatalf("missing tracked book was not retried: %v", audible.downloaded)
	}
}

func TestDownload_UsesNoSeriesPrefixWithoutASeries(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}
	fs.files["media/Standalone.m4b"] = []byte("ready")
	audible := &fakeAudible{library: []Book{{ASIN: "B001", Title: "Standalone", SeriesSequence: "1"}}}
	app := &App{Audible: audible, FS: fs, Store: &fakeStore{asins: []string{"B001"}}, MediaDir: "media"}

	if err := app.Download(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(audible.downloaded) != 0 {
		t.Fatalf("standalone book was unexpectedly redownloaded: %v", audible.downloaded)
	}
}

func TestDownload_RetriesTrackedAAXCWithoutVoucher(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}
	fs.files["media/Locked Book.aaxc"] = []byte("encrypted")
	audible := &fakeAudible{library: []Book{{ASIN: "B001", Title: "Locked Book"}}}
	app := &App{Audible: audible, FS: fs, Store: &fakeStore{asins: []string{"B001"}}, MediaDir: "media"}

	if err := app.Download(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(audible.downloaded, []string{"B001"}) {
		t.Fatalf("unconvertible AAXC was not retried: %v", audible.downloaded)
	}
}

func TestDownload_RetriesTrackedBookWithStalePartsManifest(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}
	data, err := json.Marshal(partsManifest{ASIN: "B001", Parts: []string{"B002"}})
	if err != nil {
		t.Fatal(err)
	}
	fs.files["media/B001-parts.json"] = data
	audible := &fakeAudible{library: []Book{{ASIN: "B001", Title: "Split Book"}}}
	app := &App{Audible: audible, FS: fs, Store: &fakeStore{asins: []string{"B001"}}, MediaDir: "media"}

	if err := app.Download(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(audible.downloaded, []string{"B001"}) {
		t.Fatalf("stale parts manifest was not retried: %v", audible.downloaded)
	}
}

func TestDownload_CreatesSeriesDirectory(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}
	fs.entries["media/My Series"] = []os.DirEntry{}

	store := &fakeStore{}
	audible := &fakeAudible{
		library: []Book{
			{ASIN: "B003", Title: "Series Book", SeriesTitle: "My Series", SeriesSequence: "2"},
		},
	}
	app := &App{
		Audible:  audible,
		FS:       fs,
		Store:    store,
		MediaDir: "media",
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
		Audible:  audible,
		FS:       fs,
		Store:    store,
		MediaDir: "media",
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
		Audible:  audible,
		FS:       fs,
		Store:    store,
		MediaDir: "media",
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
		Audible:  audible,
		FS:       fs,
		Store:    store,
		MediaDir: "media",
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
		Audible:  &fakeAudible{library: []Book{{ASIN: "B001", Title: "Book"}}},
		FS:       fs,
		Store:    &fakeStore{saveErr: errors.New("disk full")},
		MediaDir: "media",
	}

	if err := app.Download(context.Background()); err == nil {
		t.Fatal("expected store failures to be returned")
	}
}

func TestDownload_ReturnsPostProcessingErrors(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["media"] = true
	fs.entries["media"] = []os.DirEntry{}

	store := &fakeStore{}
	app := &App{
		Audible: &fakeAudible{
			library:       []Book{{ASIN: "B001", Title: "Book"}},
			audioPartsErr: errors.New("parts lookup failed"),
		},
		FS:       fs,
		Store:    store,
		MediaDir: "media",
	}

	if err := app.Download(context.Background()); err == nil {
		t.Fatal("expected post-processing failures to be returned")
	}
	if len(store.asins) != 0 {
		t.Fatalf("failed post-processing must remain retryable, recorded %v", store.asins)
	}
}

func TestSyncConvertsExistingMediaAfterADownloadFailure(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/existing.aax"] = []byte("aax")
	converter := &fakeConverter{fs: fs}
	app := &App{
		Audible:   &fakeAudible{library: []Book{{ASIN: "B001", Title: "New Book"}}, downloadErr: errors.New("network"), activationBytes: "a1b2c3d4"},
		Converter: converter,
		FS:        fs,
		Store:     &fakeStore{},
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}

	if err := sync(context.Background(), app); err == nil {
		t.Fatal("expected the failed download to be reported")
	}
	if _, ok := fs.files["media/existing.m4b"]; !ok {
		t.Fatal("existing media was not converted after the download failure")
	}
}

func TestConvert_CallsConverterWithActivationBytes(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/book1.aax"] = []byte("aax data")
	fs.files["media/book2.aaxc"] = []byte("aaxc data")
	fs.files["media/book2.voucher"] = []byte(`{"content_license":{"license_response":{"key":"key1","iv":"iv1"}}}`)

	converter := &fakeConverter{fs: fs}
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
	converter := &fakeConverter{fs: fs}
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

func TestConvert_CommitsOutputAtomically(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/book.aax"] = []byte("aax data")
	converter := &fakeConverter{fs: fs, writeOutput: true}
	app := &App{
		Audible:   &fakeAudible{activationBytes: "a1b2c3d4"},
		Converter: converter,
		FS:        fs,
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}

	if err := app.Convert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := converter.aaxCalls[0].output; got != "media/book.partial.m4b" {
		t.Fatalf("converter output %q must retain the .m4b extension", got)
	}
	if _, ok := fs.files["media/book.m4b"]; !ok {
		t.Fatal("completed conversion was not committed")
	}
	if _, ok := fs.files["media/book.aax"]; ok {
		t.Fatal("committed conversion did not release its source file")
	}
	if _, ok := fs.files["media/book.partial.m4b"]; ok {
		t.Fatal("partial output remains after commit")
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
	fs.files["media/B0DWPNQK52.aax"] = []byte("source1")
	fs.files["media/B0DWP16T86.aaxc"] = []byte("source2")
	fs.files["media/B0DWP16T86.voucher"] = []byte("voucher2")

	converter := &fakeConverter{fs: fs}
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
	if converter.concatCalls[0].output != "media/This Inevitable Ruin.partial.m4b" {
		t.Fatalf("unexpected concat output: %s", converter.concatCalls[0].output)
	}
	if _, ok := fs.files["media/This Inevitable Ruin.m4b"]; !ok {
		t.Fatal("merged book was not atomically committed")
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
	for _, path := range []string{"media/B0DWPNQK52.aax", "media/B0DWP16T86.aaxc", "media/B0DWP16T86.voucher"} {
		if _, ok := fs.files[path]; ok {
			t.Fatalf("expected merged-part source %s to be removed", path)
		}
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

func TestConvert_DoesNotOverwriteMergedBook(t *testing.T) {
	fs := newFakeFS()
	manifestData, err := json.Marshal(partsManifest{
		ASIN:       "B001",
		Parts:      []string{"B002", "B003"},
		OutputBase: "Existing Book",
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	fs.files["media/B001-parts.json"] = manifestData
	fs.files["media/B002.m4b"] = []byte("part 1")
	fs.files["media/B003.m4b"] = []byte("part 2")
	fs.files["media/Existing Book.m4b"] = []byte("finished")
	converter := &fakeConverter{}

	app := &App{
		Audible:   &fakeAudible{},
		Converter: converter,
		FS:        fs,
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}
	if err := app.Convert(context.Background()); err == nil {
		t.Fatal("expected existing merged book to be preserved")
	}
	if len(converter.concatCalls) != 0 {
		t.Fatal("expected ffmpeg concat not to run")
	}
	for _, path := range []string{"media/B002.m4b", "media/B003.m4b"} {
		if _, ok := fs.files[path]; !ok {
			t.Fatalf("expected part %s to be preserved", path)
		}
	}
}

func TestConvert_RejectsUnsafePartASIN(t *testing.T) {
	fs := newFakeFS()
	manifestData, err := json.Marshal(partsManifest{
		ASIN:       "B001",
		Parts:      []string{"../outside"},
		OutputBase: "Book",
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	fs.files["media/B001-parts.json"] = manifestData

	app := &App{
		Audible:   &fakeAudible{},
		Converter: &fakeConverter{},
		FS:        fs,
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}
	if err := app.Convert(context.Background()); err == nil {
		t.Fatal("expected unsafe part ASIN to be rejected")
	}
}

func TestConvert_RejectsUnsafeMultipartOutput(t *testing.T) {
	fs := newFakeFS()
	data, err := json.Marshal(partsManifest{
		ASIN:       "B001",
		Parts:      []string{"B002"},
		OutputBase: "../outside",
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.files["media/B001-parts.json"] = data
	fs.files["media/B002.m4b"] = []byte("part")
	app := &App{
		Audible:   &fakeAudible{},
		Converter: &fakeConverter{},
		FS:        fs,
		MediaDir:  "media",
		lookPath:  func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
	}

	if err := app.Convert(context.Background()); err == nil {
		t.Fatal("expected path-traversing multipart output to be rejected")
	}
	if _, ok := fs.files["media/B002.m4b"]; !ok {
		t.Fatal("part was removed after rejecting unsafe manifest")
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

func TestClean_RemovesOnlyInputsForCompletedBooks(t *testing.T) {
	fs := newFakeFS()
	fs.files["media/done.aax"] = []byte("x")
	fs.files["media/done.voucher"] = []byte("x")
	fs.files["media/done-chapters.json"] = []byte("x")
	fs.files["media/done.m4b"] = []byte("x")
	fs.files["media/pending.aaxc"] = []byte("x")
	fs.files["media/pending.voucher"] = []byte("x")
	fs.files["media/book.jpg"] = []byte("x")
	fs.files["media/book.json"] = []byte("x")
	fs.files["media/book.pdf"] = []byte("x")

	app := &App{FS: fs, MediaDir: "media"}
	if err := app.Clean(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, path := range []string{"media/done.aax", "media/done.voucher", "media/done-chapters.json"} {
		if _, ok := fs.files[path]; ok {
			t.Fatalf("expected completed-book input %s to be removed", path)
		}
	}
	for _, path := range []string{"media/done.m4b", "media/pending.aaxc", "media/pending.voucher", "media/book.jpg", "media/book.json", "media/book.pdf"} {
		if _, ok := fs.files[path]; !ok {
			t.Fatalf("expected %s to be preserved", path)
		}
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
