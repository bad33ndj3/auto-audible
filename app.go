package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// App orchestrates the audible download and conversion workflow.
type App struct {
	Audible         AudibleService
	Converter       MediaConverter
	FS              FileSystem
	Store           ASINStore
	MediaIndex      MediaIndexStore
	Prompter        Prompter
	MediaDir        string
	DownloadWorkers int
	Progress        ProgressReporter
	lookPath        func(string) (string, error)
	interactive     func() bool
	downloadStart   time.Time
}

func (a *App) progress() ProgressReporter {
	if a.Progress == nil {
		return &noopProgress{}
	}
	return a.Progress
}

func (a *App) EnsureAudibleConfigured(ctx context.Context) error {
	lp := a.lookPath
	if lp == nil {
		lp = exec.LookPath
	}
	if _, err := lp("audible"); err != nil {
		return fmt.Errorf("audible-cli was not found on PATH")
	}

	hasProfiles, err := a.Audible.HasProfile(ctx)
	if err == nil && hasProfiles {
		return nil
	}

	interactive := a.interactive
	if interactive == nil {
		interactive = isInteractive
	}
	if !interactive() {
		return fmt.Errorf("audible-cli does not appear configured; run `audible quickstart` once and rerun this command")
	}

	shouldRun, promptErr := a.Prompter.PromptYesNo("audible-cli does not appear configured. Run `audible quickstart` now? [y/N]: ", false)
	if promptErr != nil {
		return fmt.Errorf("failed to confirm audible quickstart state: %w", promptErr)
	}
	if !shouldRun {
		return fmt.Errorf("audible-cli is required; run `audible quickstart` and rerun this command")
	}

	if err := a.Audible.RunQuickstart(ctx); err != nil {
		return fmt.Errorf("audible quickstart failed: %w", err)
	}

	hasProfiles, err = a.Audible.HasProfile(ctx)
	if err != nil {
		return fmt.Errorf("failed to validate audible quickstart: %w", err)
	}
	if !hasProfiles {
		return fmt.Errorf("audible quickstart finished, but no profile was detected")
	}

	return nil
}

func (a *App) Download(ctx context.Context) error {
	if err := a.FS.MkdirAll(a.MediaDir, 0o755); err != nil {
		return fmt.Errorf("failed to create media directory: %w", err)
	}

	if err := a.syncMediaIndex(); err != nil {
		a.progress().Log(fmt.Sprintf("Warning: failed to sync media index: %v", err))
	}

	items, err := a.Audible.ExportLibrary(ctx)
	if err != nil {
		return err
	}

	if len(items) == 0 {
		a.progress().Log("No library items found in Audible export")
		return nil
	}

	a.progress().SetLibrary(items)

	downloaded, err := a.Store.Load()
	if err != nil {
		return fmt.Errorf("failed to load downloaded ASINs: %w", err)
	}

	downloadedSet := make(map[string]struct{}, len(downloaded))
	for _, asin := range downloaded {
		downloadedSet[asin] = struct{}{}
	}

	// Sync store against actual media files on disk.
	// If a .m4b exists for a book, ensure its ASIN is in the store.
	// If an ASIN is in the store but no media exists, remove it.
	changed := false
	newDownloaded := make([]string, 0, len(items))
	for _, item := range items {
		asin := item.ASIN
		if a.hasM4BForBook(item) {
			if _, ok := downloadedSet[asin]; !ok {
				downloadedSet[asin] = struct{}{}
				changed = true
			}
			newDownloaded = append(newDownloaded, asin)
			a.progress().UpdateBook(BookProgress{ASIN: asin, Title: item.Title, State: StateReady})
		} else if _, ok := downloadedSet[asin]; ok {
			// ASIN in store but no media -> remove from store
			changed = true
			a.progress().UpdateBook(BookProgress{ASIN: asin, Title: item.Title, State: StatePending})
		} else {
			a.progress().UpdateBook(BookProgress{ASIN: asin, Title: item.Title, State: StatePending})
		}
	}
	if changed {
		if saveErr := a.Store.Save(newDownloaded); saveErr != nil {
			a.progress().Log(fmt.Sprintf("Warning: failed to update downloaded ASINs store: %v", saveErr))
		}
		downloaded = newDownloaded
		downloadedSet = make(map[string]struct{}, len(downloaded))
		for _, asin := range downloaded {
			downloadedSet[asin] = struct{}{}
		}
	}

	type job struct {
		item      Book
		outputDir string
		hasSeries bool
	}

	var jobs []job
	for _, item := range items {
		asin := item.ASIN
		if _, ok := downloadedSet[asin]; ok {
			a.progress().Log(fmt.Sprintf("ASIN %s already downloaded, skipping.", asin))
			continue
		}

		outputDir := a.MediaDir
		seriesTitle := strings.TrimSpace(item.SeriesTitle)
		hasSeries := seriesTitle != ""
		if hasSeries {
			seriesDir := filepath.Join(a.MediaDir, sanitizeFileName(seriesTitle))
			if err := a.FS.MkdirAll(seriesDir, 0o755); err != nil {
				return fmt.Errorf("failed to create series directory: %w", err)
			}
			outputDir = seriesDir
		}

		jobs = append(jobs, job{item: item, outputDir: outputDir, hasSeries: hasSeries})
	}

	workers := a.DownloadWorkers
	if workers <= 0 {
		workers = 4
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	jobCh := make(chan job)

	pending := len(jobs)
	completed := 0
	failed := 0

	a.downloadStart = time.Now()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				item := j.item
				asin := item.ASIN

				a.progress().UpdateBook(BookProgress{ASIN: asin, Title: item.Title, State: StateDownloading})
				a.progress().Log(fmt.Sprintf("Downloading ASIN %s (%s)", asin, item.Title))
				if err := a.Audible.DownloadBook(ctx, asin, j.outputDir); err != nil {
					a.progress().Log(fmt.Sprintf("Failed to download ASIN %s: %v", asin, err))
					a.progress().UpdateBook(BookProgress{ASIN: asin, Title: item.Title, State: StateError, Message: err.Error()})
					mu.Lock()
					failed++
					a.updateDownloadStats(pending, completed, failed)
					mu.Unlock()
					continue
				}

				if err := a.renameDownloadedFiles(j.outputDir, asin, item.Title, j.hasSeries, item.SeriesSequence); err != nil {
					a.progress().Log(fmt.Sprintf("Failed to rename files for ASIN %s: %v", asin, err))
				}

				if err := a.trackMediaIndex(asin, item.Title, item.SeriesTitle, item.SeriesSequence); err != nil {
					a.progress().Log(fmt.Sprintf("Failed to update media index for ASIN %s: %v", asin, err))
				}

				mu.Lock()
				downloaded = append(downloaded, asin)
				downloadedSet[asin] = struct{}{}
				if err := a.Store.Save(downloaded); err != nil {
					a.progress().Log(fmt.Sprintf("Failed to save downloaded ASINs: %v", err))
				}
				completed++
				a.updateDownloadStats(pending, completed, failed)
				mu.Unlock()

				// Determine post-download state by checking files.
				info := a.mediaInfoForBase(j.outputDir, sanitizeFileName(item.Title))
				if info.HasM4B {
					a.progress().UpdateBook(BookProgress{ASIN: asin, Title: item.Title, State: StateReady})
				} else if info.HasAAX || info.HasAAXC {
					a.progress().UpdateBook(BookProgress{ASIN: asin, Title: item.Title, State: StateNeedsConvert})
				} else {
					a.progress().UpdateBook(BookProgress{ASIN: asin, Title: item.Title, State: StateDownloaded})
				}
			}
		}()
	}

	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()

	a.progress().Done()
	return nil
}

func (a *App) updateDownloadStats(pending, completed, failed int) {
	total := pending + completed + failed
	a.progress().SetStats(total, completed, 0, 0, pending-failed, failed)

	percent := 0
	if total > 0 {
		percent = int(float64(completed+failed) / float64(total) * 100)
	}
	var speed string
	if !a.downloadStart.IsZero() && (completed+failed) > 0 {
		elapsed := time.Since(a.downloadStart)
		bpm := float64(completed+failed) / elapsed.Minutes()
		speed = fmt.Sprintf("%d/%d done | ~%.1f books/min", completed+failed, total, bpm)
	} else {
		speed = fmt.Sprintf("%d/%d", completed+failed, total)
	}
	a.progress().SetOverallProgress(percent, speed)
}

func (a *App) trackMediaIndex(asin, title, seriesTitle string, seriesSeq interface{}) error {
	if a.MediaIndex == nil {
		return nil
	}
	index, err := a.MediaIndex.Load()
	if err != nil {
		return err
	}
	if index == nil {
		index = make(MediaIndex)
	}

	// Determine the directory and base name that the book should live at.
	dir := a.MediaDir
	base := sanitizeFileName(title)
	if strings.TrimSpace(seriesTitle) != "" {
		dir = filepath.Join(a.MediaDir, sanitizeFileName(seriesTitle))
		base = formatPrefix(seriesSeq) + base
	}

	// Find which final media file actually exists for this ASIN.
	var foundPath string
	for _, ext := range []string{".m4b", ".aax", ".aaxc"} {
		p := filepath.Join(dir, base+ext)
		if fileExistsFS(a.FS, p) {
			rel, _ := filepath.Rel(a.MediaDir, p)
			foundPath = rel
			break
		}
	}
	if foundPath == "" {
		// The rename may have added a collision suffix; scan the directory.
		entries, err := a.FS.ReadDir(dir)
		if err == nil {
			for _, entry := range entries {
				name := entry.Name()
				noExt := strings.TrimSuffix(name, filepath.Ext(name))
				if strings.HasPrefix(noExt, base) {
					ext := strings.ToLower(filepath.Ext(name))
					if ext == ".m4b" || ext == ".aax" || ext == ".aaxc" {
						rel, _ := filepath.Rel(a.MediaDir, filepath.Join(dir, name))
						foundPath = rel
						break
					}
				}
			}
		}
	}
	if foundPath == "" {
		// Last fallback: search the whole media tree for any file starting with the raw ASIN.
		_ = a.FS.WalkDir(a.MediaDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			baseName := strings.TrimSuffix(name, filepath.Ext(name))
			if baseName == asin || strings.HasPrefix(baseName, asin+"_") || strings.HasPrefix(baseName, asin+"-") {
				rel, _ := filepath.Rel(a.MediaDir, path)
				foundPath = rel
				return filepath.SkipAll
			}
			return nil
		})
	}
	if foundPath == "" {
		return nil
	}

	index[asin] = MediaEntry{
		Path:         foundPath,
		Title:        title,
		Series:       seriesTitle,
		DownloadedAt: time.Now().UTC(),
	}
	return a.MediaIndex.Save(index)
}

func (a *App) syncMediaIndex() error {
	if a.MediaIndex == nil {
		return nil
	}
	index, err := a.MediaIndex.Load()
	if err != nil {
		return err
	}
	changed := false
	for asin, entry := range index {
		if !fileExistsFS(a.FS, filepath.Join(a.MediaDir, entry.Path)) {
			delete(index, asin)
			changed = true
		}
	}
	if changed {
		return a.MediaIndex.Save(index)
	}
	return nil
}

func (a *App) renameDownloadedFiles(outputDir, asin, title string, hasSeries bool, seriesSeq interface{}) error {
	files, err := a.FS.ReadDir(outputDir)
	if err != nil {
		return err
	}

	prefix := ""
	if hasSeries {
		prefix = formatPrefix(seriesSeq)
	}

	sanitizedTitle := sanitizeFileName(title)

	for _, file := range files {
		name := file.Name()
		base := strings.TrimSuffix(name, filepath.Ext(name))

		if !strings.HasPrefix(base, asin) {
			continue
		}
		if len(base) > len(asin) {
			nextChar := base[len(asin)]
			if nextChar != '_' && nextChar != '-' {
				continue
			}
		}

		ext := filepath.Ext(name)
		suffix := ""
		if len(base) > len(asin) {
			suffix = base[len(asin):]
		}

		newName := prefix + sanitizedTitle + suffix + ext

		newPath := filepath.Join(outputDir, newName)
		if fileExistsFS(a.FS, newPath) && name != newName {
			counter := 1
			for {
				altName := prefix + sanitizedTitle + fmt.Sprintf("_%d", counter) + ext
				altPath := filepath.Join(outputDir, altName)
				if !fileExistsFS(a.FS, altPath) {
					newName = altName
					newPath = altPath
					break
				}
				counter++
				if counter > 100 {
					return fmt.Errorf("could not find unique name for %s", name)
				}
			}
		}

		oldPath := filepath.Join(outputDir, name)
		if name == newName {
			continue
		}
		if err := a.FS.Rename(oldPath, newPath); err != nil {
			return fmt.Errorf("failed to rename %s to %s: %w", name, newName, err)
		}
	}

	return nil
}

func (a *App) updateMediaIndexPath(oldPath, newPath string) error {
	if a.MediaIndex == nil {
		return nil
	}
	index, err := a.MediaIndex.Load()
	if err != nil {
		return err
	}
	changed := false
	oldRel, _ := filepath.Rel(a.MediaDir, oldPath)
	newRel, _ := filepath.Rel(a.MediaDir, newPath)
	for asin, entry := range index {
		if entry.Path == oldRel {
			entry.Path = newRel
			index[asin] = entry
			changed = true
			break
		}
	}
	if changed {
		return a.MediaIndex.Save(index)
	}
	return nil
}

func (a *App) Convert(ctx context.Context) error {
	lp := a.lookPath
	if lp == nil {
		lp = exec.LookPath
	}
	if _, err := lp("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg was not found on PATH; install ffmpeg or use the Docker image for conversion")
	}

	aaxFiles, err := findFilesRecursiveFS(a.FS, a.MediaDir, ".aax")
	if err != nil {
		return fmt.Errorf("failed to list .aax files: %w", err)
	}
	aaxcFiles, err := findFilesRecursiveFS(a.FS, a.MediaDir, ".aaxc")
	if err != nil {
		return fmt.Errorf("failed to list .aaxc files: %w", err)
	}

	if len(aaxFiles) == 0 && len(aaxcFiles) == 0 {
		a.progress().Log("No .aax or .aaxc files found to convert")
		return nil
	}

	activationBytes := ""
	if len(aaxFiles) > 0 {
		activationBytes, err = a.Audible.GetActivationBytes(ctx)
		if err != nil {
			return err
		}
	}

	// Build a synthetic library for the TUI from files to convert.
	allFiles := append(aaxFiles, aaxcFiles...)
	for _, path := range allFiles {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		a.progress().UpdateBook(BookProgress{ASIN: name, Title: name, State: StateNeedsConvert})
	}

	failed := 0
	converted := 0
	total := len(allFiles)

	for _, inputPath := range aaxFiles {
		outputPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".m4b"
		name := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
		a.progress().UpdateBook(BookProgress{ASIN: name, Title: name, State: StateConverting})
		a.progress().Log(fmt.Sprintf("Converting %s -> %s", inputPath, outputPath))
		if err := a.Converter.ConvertAAX(ctx, inputPath, outputPath, activationBytes); err != nil {
			failed++
			a.progress().Log(fmt.Sprintf("Failed to convert %s: %v", inputPath, err))
			a.progress().UpdateBook(BookProgress{ASIN: name, Title: name, State: StateError, Message: err.Error()})
		} else {
			converted++
			a.progress().UpdateBook(BookProgress{ASIN: name, Title: name, State: StateReady})
			if err := a.updateMediaIndexPath(inputPath, outputPath); err != nil {
				a.progress().Log(fmt.Sprintf("Failed to update media index after convert: %v", err))
			}
		}
		a.progress().SetStats(total, converted, 0, total-converted-failed, 0, failed)
	}

	for _, inputPath := range aaxcFiles {
		outputPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".m4b"
		voucherPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".voucher"
		name := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))

		key, iv, keyErr := a.loadVoucherKeyIV(voucherPath)
		if keyErr != nil {
			failed++
			a.progress().Log(fmt.Sprintf("Failed to load voucher for %s: %v", inputPath, keyErr))
			a.progress().UpdateBook(BookProgress{ASIN: name, Title: name, State: StateError, Message: keyErr.Error()})
			a.progress().SetStats(total, converted, 0, total-converted-failed, 0, failed)
			continue
		}

		a.progress().UpdateBook(BookProgress{ASIN: name, Title: name, State: StateConverting})
		a.progress().Log(fmt.Sprintf("Converting %s -> %s", inputPath, outputPath))
		if err := a.Converter.ConvertAAXC(ctx, inputPath, outputPath, key, iv); err != nil {
			failed++
			a.progress().Log(fmt.Sprintf("Failed to convert %s: %v", inputPath, err))
			a.progress().UpdateBook(BookProgress{ASIN: name, Title: name, State: StateError, Message: err.Error()})
		} else {
			converted++
			a.progress().UpdateBook(BookProgress{ASIN: name, Title: name, State: StateReady})
			if err := a.updateMediaIndexPath(inputPath, outputPath); err != nil {
				a.progress().Log(fmt.Sprintf("Failed to update media index after convert: %v", err))
			}
		}
		a.progress().SetStats(total, converted, 0, total-converted-failed, 0, failed)
	}

	a.progress().Done()
	if failed > 0 {
		return fmt.Errorf("failed to convert %d file(s)", failed)
	}
	return nil
}

func (a *App) loadVoucherKeyIV(voucherPath string) (string, string, error) {
	data, err := a.FS.ReadFile(voucherPath)
	if err != nil {
		return "", "", err
	}
	var voucher voucherFile
	if err := json.Unmarshal(data, &voucher); err != nil {
		return "", "", err
	}
	key := strings.TrimSpace(voucher.ContentLicense.LicenseResponse.Key)
	iv := strings.TrimSpace(voucher.ContentLicense.LicenseResponse.IV)
	if key == "" || iv == "" {
		return "", "", fmt.Errorf("voucher does not contain a valid key/iv")
	}
	return key, iv, nil
}

func (a *App) Clean(ctx context.Context) error {
	err := a.FS.WalkDir(a.MediaDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".aaxc") ||
			strings.HasSuffix(name, ".aax") ||
			strings.HasSuffix(name, ".jpg") ||
			strings.HasSuffix(name, ".json") ||
			strings.HasSuffix(name, ".voucher") ||
			strings.HasSuffix(name, ".pdf") {
			if err := a.FS.Remove(path); err != nil {
				return fmt.Errorf("failed to remove %s: %w", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to clean media directory: %w", err)
	}
	if err := a.syncMediaIndex(); err != nil {
		return fmt.Errorf("failed to sync media index after clean: %w", err)
	}
	return nil
}

func (a *App) Status(ctx context.Context, showTable bool) error {
	items, err := a.Audible.ExportLibrary(ctx)
	if err != nil {
		return err
	}

	downloaded, err := a.Store.Load()
	if err != nil {
		return fmt.Errorf("failed to load downloaded ASINs: %w", err)
	}
	downloadedSet := make(map[string]struct{}, len(downloaded))
	for _, asin := range downloaded {
		downloadedSet[asin] = struct{}{}
	}

	state, err := a.scanMediaState()
	if err != nil {
		return err
	}
	asinMediaIndex, err := a.buildASINMediaIndex()
	if err != nil {
		return err
	}

	librarySet := make(map[string]struct{}, len(items))
	for _, item := range items {
		asin := strings.TrimSpace(item.ASIN)
		if asin == "" {
			continue
		}
		librarySet[asin] = struct{}{}
	}

	trackedInLibrary := 0
	for _, asin := range downloaded {
		if _, ok := librarySet[asin]; ok {
			trackedInLibrary++
		}
	}

	remaining := len(librarySet) - trackedInLibrary
	if remaining < 0 {
		remaining = 0
	}

	fmt.Println("Library status")
	fmt.Printf("Total in Audible library: %d\n", len(librarySet))
	fmt.Printf("Tracked downloaded ASINs: %d\n", len(downloaded))
	fmt.Printf("Tracked and still in library: %d\n", trackedInLibrary)
	fmt.Printf("Remaining to download: %d\n", remaining)
	fmt.Printf("Ready to listen (.m4b): %d\n", len(state.Ready))
	fmt.Printf("Needs conversion (.aax): %d\n", len(state.NeedsAAX))
	fmt.Printf("Needs conversion (.aaxc): %d\n", len(state.NeedsAAXC))

	if showTable {
		rows := make([][3]string, 0, len(items))
		for _, item := range items {
			asin := strings.TrimSpace(item.ASIN)
			if asin == "" {
				continue
			}
			_, tracked := downloadedSet[asin]
			bookState := computeBookState(tracked, asinMediaIndex[asin])
			rows = append(rows, [3]string{asin, strings.TrimSpace(item.Title), bookState})
		}

		fmt.Println()
		fmt.Println("Books")
		printStatusTable(rows)
	}

	return nil
}

func (a *App) ReadySummary() error {
	state, err := a.scanMediaState()
	if err != nil {
		return err
	}

	fmt.Printf("\nReady-to-listen summary for %s\n", a.MediaDir)
	fmt.Printf("Ready (.m4b): %d\n", len(state.Ready))
	for _, path := range state.Ready {
		fmt.Printf("  - %s\n", path)
	}

	fmt.Printf("Needs conversion (.aax): %d\n", len(state.NeedsAAX))
	for _, path := range state.NeedsAAX {
		fmt.Printf("  - %s\n", path)
	}
	fmt.Printf("Needs conversion (.aaxc): %d\n", len(state.NeedsAAXC))
	for _, path := range state.NeedsAAXC {
		fmt.Printf("  - %s\n", path)
	}

	return nil
}

func (a *App) scanMediaState() (MediaState, error) {
	hasM4B := map[string]struct{}{}
	hasAAX := map[string]struct{}{}
	hasAAXC := map[string]struct{}{}

	err := a.FS.WalkDir(a.MediaDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(a.MediaDir, path)
		name := d.Name()
		ext := strings.ToLower(filepath.Ext(name))
		base := strings.TrimSuffix(relPath, ext)
		if base == "" {
			return nil
		}

		switch ext {
		case ".m4b":
			hasM4B[base] = struct{}{}
		case ".aax":
			hasAAX[base] = struct{}{}
		case ".aaxc":
			hasAAXC[base] = struct{}{}
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return MediaState{}, nil
		}
		return MediaState{}, fmt.Errorf("failed to read media directory: %w", err)
	}

	state := MediaState{}
	for base := range hasM4B {
		state.Ready = append(state.Ready, base)
	}
	for base := range hasAAX {
		if _, ok := hasM4B[base]; ok {
			continue
		}
		state.NeedsAAX = append(state.NeedsAAX, base)
	}
	for base := range hasAAXC {
		if _, ok := hasM4B[base]; ok {
			continue
		}
		state.NeedsAAXC = append(state.NeedsAAXC, base)
	}

	sort.Strings(state.Ready)
	sort.Strings(state.NeedsAAX)
	sort.Strings(state.NeedsAAXC)

	return state, nil
}

func (a *App) buildASINMediaIndex() (map[string]MediaInfo, error) {
	index := map[string]MediaInfo{}

	err := a.FS.WalkDir(a.MediaDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".voucher") {
			return nil
		}
		data, readErr := a.FS.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var voucher voucherFile
		if unmarshalErr := json.Unmarshal(data, &voucher); unmarshalErr != nil {
			return nil
		}
		asin := strings.TrimSpace(voucher.ContentLicense.ASIN)
		if asin == "" {
			return nil
		}
		format := strings.TrimSpace(voucher.ContentLicense.ContentMetadata.ContentReference.ContentFormat)
		dir := filepath.Dir(path)
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		candidates := []string{base}
		if trimmed := trimCodecSuffix(base, format); trimmed != "" && trimmed != base {
			candidates = append(candidates, trimmed)
		}
		info := index[asin]
		for _, candidate := range candidates {
			if candidate == "" {
				continue
			}
			info = mergeMediaInfo(info, a.mediaInfoForBase(dir, candidate))
		}
		index[asin] = info
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list voucher files: %w", err)
	}

	err = a.FS.WalkDir(a.MediaDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), "-chapters.json") {
			return nil
		}
		data, readErr := a.FS.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var chapter chapterFile
		if unmarshalErr := json.Unmarshal(data, &chapter); unmarshalErr != nil {
			return nil
		}
		asin := strings.TrimSpace(chapter.ContentMetadata.ContentReference.ASIN)
		if asin == "" {
			return nil
		}
		format := strings.TrimSpace(chapter.ContentMetadata.ContentReference.ContentFormat)
		dir := filepath.Dir(path)
		base := strings.TrimSuffix(filepath.Base(path), "-chapters.json")
		candidates := []string{base}
		if format != "" {
			candidates = append(candidates, base+"-"+format)
		}
		info := index[asin]
		for _, candidate := range candidates {
			if candidate == "" {
				continue
			}
			info = mergeMediaInfo(info, a.mediaInfoForBase(dir, candidate))
		}
		index[asin] = info
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list chapter metadata files: %w", err)
	}

	return index, nil
}

func (a *App) mediaInfoForBase(dir, base string) MediaInfo {
	return MediaInfo{
		HasM4B:  fileExistsFS(a.FS, filepath.Join(dir, base+".m4b")),
		HasAAX:  fileExistsFS(a.FS, filepath.Join(dir, base+".aax")),
		HasAAXC: fileExistsFS(a.FS, filepath.Join(dir, base+".aaxc")),
	}
}

func fileExistsFS(fs FileSystem, path string) bool {
	_, err := fs.Stat(path)
	return err == nil
}

func findFilesRecursiveFS(fs FileSystem, root, ext string) ([]string, error) {
	var files []string
	err := fs.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.ToLower(filepath.Ext(path)) == ext {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func (a *App) hasM4BForBook(book Book) bool {
	// Prefer the media index when available, but only if it points to a .m4b.
	if a.MediaIndex != nil {
		index, err := a.MediaIndex.Load()
		if err == nil {
			if entry, ok := index[book.ASIN]; ok {
				if strings.HasSuffix(strings.ToLower(entry.Path), ".m4b") &&
					fileExistsFS(a.FS, filepath.Join(a.MediaDir, entry.Path)) {
					return true
				}
			}
		}
	}

	seriesTitle := strings.TrimSpace(book.SeriesTitle)
	dir := a.MediaDir
	base := sanitizeFileName(book.Title)
	if seriesTitle != "" {
		dir = filepath.Join(a.MediaDir, sanitizeFileName(seriesTitle))
		base = formatPrefix(book.SeriesSequence) + base
	}
	// Check renamed file first, then fallback to raw ASIN filename.
	if fileExistsFS(a.FS, filepath.Join(dir, base+".m4b")) {
		return true
	}
	return fileExistsFS(a.FS, filepath.Join(a.MediaDir, book.ASIN+".m4b"))
}

func isInteractive() bool {
	stdin, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	stdout, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (stdin.Mode()&os.ModeCharDevice) != 0 && (stdout.Mode()&os.ModeCharDevice) != 0
}
