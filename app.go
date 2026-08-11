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
)

// App orchestrates the audible download and conversion workflow.
type App struct {
	Audible     AudibleService
	Converter   MediaConverter
	FS          FileSystem
	Store       ASINStore
	Prompter    Prompter
	MediaDir    string
	lookPath    func(string) (string, error)
	interactive func() bool
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
	if err != nil {
		return fmt.Errorf("failed to inspect audible-cli profiles: %w", err)
	}
	if hasProfiles {
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

	items, err := a.Audible.ExportLibrary(ctx)
	if err != nil {
		return err
	}

	if len(items) == 0 {
		fmt.Println("No library items found in Audible export")
		return nil
	}

	downloaded, err := a.Store.Load()
	if err != nil {
		return fmt.Errorf("failed to load downloaded ASINs: %w", err)
	}

	downloadedSet := make(map[string]struct{}, len(downloaded))
	for _, asin := range downloaded {
		downloadedSet[asin] = struct{}{}
	}

	type job struct {
		item      Book
		outputDir string
		hasSeries bool
	}

	var jobs []job
	for _, item := range items {
		asin := item.ASIN
		if _, ok := downloadedSet[asin]; ok && a.hasBookMedia(item) {
			fmt.Printf("ASIN %s already downloaded, skipping.\n", asin)
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

	failed := 0
	for _, j := range jobs {
		item, asin := j.item, j.item.ASIN
		fmt.Printf("Downloading new book with ASIN: %s (%s)\n", asin, item.Title)
		if err := a.Audible.DownloadBook(ctx, asin, j.outputDir); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to download ASIN %s: %v\n", asin, err)
			failed++
			continue
		}
		if err := a.renameDownloadedFiles(j.outputDir, asin, item.Title, j.hasSeries, item.SeriesSequence); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to rename files for ASIN %s: %v\n", asin, err)
			failed++
			continue
		}
		if err := a.writePartsManifestIfSplit(ctx, j.outputDir, asin, item.Title, j.hasSeries, item.SeriesSequence); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to record audio parts for ASIN %s: %v\n", asin, err)
			failed++
			continue
		}
		next := append(append([]string(nil), downloaded...), asin)
		if err := a.Store.Save(next); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save downloaded ASINs: %v\n", err)
			failed++
			continue
		}
		downloaded = next
	}

	if failed > 0 {
		return fmt.Errorf("failed to download or record %d book(s)", failed)
	}
	return nil
}

func (a *App) hasBookMedia(book Book) bool {
	dir := a.MediaDir
	prefix := ""
	if series := strings.TrimSpace(book.SeriesTitle); series != "" {
		dir = filepath.Join(dir, sanitizeFileName(series))
		prefix = formatPrefix(book.SeriesSequence)
	}
	base := prefix + sanitizeFileName(book.Title)
	if hasConvertibleMedia(a.FS, dir, base) {
		return true
	}
	data, err := a.FS.ReadFile(partsManifestPath(dir, book.ASIN))
	if err != nil {
		return false
	}
	var manifest partsManifest
	if json.Unmarshal(data, &manifest) != nil || len(manifest.Parts) == 0 {
		return false
	}
	for _, asin := range manifest.Parts {
		if !validASIN(asin) || !hasConvertibleMedia(a.FS, dir, asin) {
			return false
		}
	}
	return true
}

func hasConvertibleMedia(fs FileSystem, dir, base string) bool {
	if fileExistsFS(fs, filepath.Join(dir, base+".m4b")) || fileExistsFS(fs, filepath.Join(dir, base+".aax")) {
		return true
	}
	return fileExistsFS(fs, filepath.Join(dir, base+".aaxc")) && fileExistsFS(fs, filepath.Join(dir, base+".voucher"))
}

func (a *App) writePartsManifestIfSplit(ctx context.Context, outputDir, asin, title string, hasSeries bool, seriesSeq SeriesSequence) error {
	if !validASIN(asin) {
		return fmt.Errorf("invalid ASIN %q", asin)
	}
	parts, err := a.Audible.GetAudioParts(ctx, asin)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		return nil
	}
	for _, partASIN := range parts {
		if !validASIN(partASIN) {
			return fmt.Errorf("invalid audio part ASIN %q", partASIN)
		}
	}

	prefix := ""
	if hasSeries {
		prefix = formatPrefix(seriesSeq)
	}
	outputBase := prefix + sanitizeFileName(title)

	manifest := partsManifest{ASIN: asin, Title: title, Parts: parts, OutputBase: outputBase}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return a.FS.WriteFile(partsManifestPath(outputDir, asin), data, 0o644)
}

func (a *App) renameDownloadedFiles(outputDir, asin, title string, hasSeries bool, seriesSeq SeriesSequence) error {
	files, err := a.FS.ReadDir(outputDir)
	if err != nil {
		return err
	}

	prefix := ""
	if hasSeries {
		prefix = formatPrefix(seriesSeq)
	}

	sanitizedTitle := sanitizeFileName(title)
	type renamePlan struct {
		oldName string
		newName string
		oldPath string
		newPath string
	}
	plans := make([]renamePlan, 0, len(files))
	targets := make(map[string]struct{}, len(files))

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

		newName := stripAudibleQualitySuffix(prefix+sanitizedTitle+suffix) + ext
		if name == newName {
			continue
		}
		oldPath := filepath.Join(outputDir, name)
		newPath := filepath.Join(outputDir, newName)
		if fileExistsFS(a.FS, newPath) {
			return fmt.Errorf("cannot rename %s: target %s already exists", name, newName)
		}
		if _, exists := targets[newPath]; exists {
			return fmt.Errorf("cannot rename %s: multiple files target %s", name, newName)
		}
		targets[newPath] = struct{}{}
		plans = append(plans, renamePlan{oldName: name, newName: newName, oldPath: oldPath, newPath: newPath})
	}

	for _, plan := range plans {
		if err := a.FS.Rename(plan.oldPath, plan.newPath); err != nil {
			return fmt.Errorf("failed to rename %s to %s: %w", plan.oldName, plan.newName, err)
		}
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

	aaxFiles = pendingConversionFiles(a.FS, aaxFiles)
	aaxcFiles = pendingConversionFiles(a.FS, aaxcFiles)
	if len(aaxFiles) == 0 && len(aaxcFiles) == 0 {
		fmt.Println("No .aax or .aaxc files found to convert")
	}

	activationBytes := ""
	if len(aaxFiles) > 0 {
		activationBytes, err = a.Audible.GetActivationBytes(ctx)
		if err != nil {
			return err
		}
	}

	failed := 0
	for _, inputPath := range aaxFiles {
		outputPath := conversionOutputPath(inputPath)
		fmt.Printf("Converting %s -> %s\n", inputPath, outputPath)
		if err := a.convertAndCommit(outputPath, func(partialPath string) error {
			return a.Converter.ConvertAAX(ctx, inputPath, partialPath, activationBytes)
		}); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "Failed to convert %s: %v\n", inputPath, err)
			continue
		}
		if err := a.FS.Remove(inputPath); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "Failed to remove converted source %s: %v\n", inputPath, err)
		}
	}

	for _, inputPath := range aaxcFiles {
		outputPath := conversionOutputPath(inputPath)
		voucherPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".voucher"

		key, iv, keyErr := a.loadVoucherKeyIV(voucherPath)
		if keyErr != nil {
			failed++
			fmt.Fprintf(os.Stderr, "Failed to load voucher for %s: %v\n", inputPath, keyErr)
			continue
		}

		fmt.Printf("Converting %s -> %s\n", inputPath, outputPath)
		if err := a.convertAndCommit(outputPath, func(partialPath string) error {
			return a.Converter.ConvertAAXC(ctx, inputPath, partialPath, key, iv)
		}); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "Failed to convert %s: %v\n", inputPath, err)
			continue
		}
		if err := a.FS.Remove(inputPath); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "Failed to remove converted source %s: %v\n", inputPath, err)
		}
	}

	if failed > 0 {
		return fmt.Errorf("failed to convert %d file(s)", failed)
	}

	if err := a.mergeParts(ctx); err != nil {
		return err
	}

	return nil
}

func (a *App) convertAndCommit(outputPath string, convert func(string) error) error {
	partialPath := temporaryOutputPath(outputPath)
	_ = a.FS.Remove(partialPath)
	committed := false
	defer func() {
		if !committed {
			_ = a.FS.Remove(partialPath)
		}
	}()
	if err := convert(partialPath); err != nil {
		return err
	}
	info, err := a.FS.Stat(partialPath)
	if err != nil {
		return fmt.Errorf("conversion produced no output: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("conversion produced an empty output")
	}
	if err := a.FS.Rename(partialPath, outputPath); err != nil {
		return fmt.Errorf("failed to commit converted output: %w", err)
	}
	committed = true
	return nil
}

func temporaryOutputPath(outputPath string) string {
	ext := filepath.Ext(outputPath)
	return strings.TrimSuffix(outputPath, ext) + ".partial" + ext
}

// mergeParts finds part manifests written during Download and, for any
// whose part .m4b files have all finished converting, concatenates them
// into a single book file and removes the intermediate parts.
func (a *App) mergeParts(ctx context.Context) error {
	manifestPaths, err := findFilesRecursiveFS(a.FS, a.MediaDir, ".json")
	if err != nil {
		return fmt.Errorf("failed to list part manifests: %w", err)
	}

	for _, manifestPath := range manifestPaths {
		if !strings.HasSuffix(manifestPath, "-parts.json") {
			continue
		}

		data, err := a.FS.ReadFile(manifestPath)
		if err != nil {
			return fmt.Errorf("failed to read part manifest %s: %w", manifestPath, err)
		}
		var manifest partsManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("failed to parse part manifest %s: %w", manifestPath, err)
		}
		if !validASIN(manifest.ASIN) {
			return fmt.Errorf("part manifest %s contains invalid ASIN %q", manifestPath, manifest.ASIN)
		}
		if manifest.OutputBase == "" || manifest.OutputBase != filepath.Base(manifest.OutputBase) || manifest.OutputBase == "." || manifest.OutputBase == ".." {
			return fmt.Errorf("part manifest %s contains invalid output name %q", manifestPath, manifest.OutputBase)
		}
		if len(manifest.Parts) == 0 {
			return fmt.Errorf("part manifest %s contains no parts", manifestPath)
		}
		seenParts := make(map[string]struct{}, len(manifest.Parts))
		for _, partASIN := range manifest.Parts {
			if !validASIN(partASIN) {
				return fmt.Errorf("part manifest %s contains invalid part ASIN %q", manifestPath, partASIN)
			}
			if _, exists := seenParts[partASIN]; exists {
				return fmt.Errorf("part manifest %s contains duplicate part ASIN %q", manifestPath, partASIN)
			}
			seenParts[partASIN] = struct{}{}
		}

		dir := filepath.Dir(manifestPath)
		partPaths := make([]string, 0, len(manifest.Parts))
		allConverted := true
		for _, partASIN := range manifest.Parts {
			partPath := filepath.Join(dir, partASIN+".m4b")
			if !fileExistsFS(a.FS, partPath) {
				allConverted = false
				break
			}
			partPaths = append(partPaths, partPath)
		}
		if !allConverted {
			continue
		}

		outputPath := filepath.Join(dir, manifest.OutputBase+".m4b")
		if fileExistsFS(a.FS, outputPath) {
			return fmt.Errorf("refusing to overwrite existing merged book %s", outputPath)
		}
		listPath := filepath.Join(dir, manifest.ASIN+"-concat.txt")

		var listBuilder strings.Builder
		for _, partPath := range partPaths {
			absPartPath, err := filepath.Abs(partPath)
			if err != nil {
				return fmt.Errorf("failed to resolve absolute path for %s: %w", partPath, err)
			}
			fmt.Fprintf(&listBuilder, "file '%s'\n", escapeFFconcatPath(absPartPath))
		}
		if err := a.FS.WriteFile(listPath, []byte(listBuilder.String()), 0o644); err != nil {
			return fmt.Errorf("failed to write concat list for %s: %w", manifest.ASIN, err)
		}

		fmt.Printf("Merging %d parts -> %s\n", len(partPaths), outputPath)
		if err := a.convertAndCommit(outputPath, func(partialPath string) error {
			return a.Converter.ConcatM4B(ctx, listPath, partialPath)
		}); err != nil {
			return fmt.Errorf("failed to merge parts for %s: %w", manifest.ASIN, err)
		}

		if err := a.FS.Remove(listPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to remove concat list %s: %v\n", listPath, err)
		}
		for _, partPath := range partPaths {
			if err := a.FS.Remove(partPath); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to remove merged part %s: %v\n", partPath, err)
			}
		}
		for _, partASIN := range manifest.Parts {
			if err := a.removeMergedPartInputs(dir, partASIN); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to remove merged inputs for %s: %v\n", partASIN, err)
			}
		}
		if err := a.FS.Remove(manifestPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to remove part manifest %s: %v\n", manifestPath, err)
		}
	}

	return nil
}

func (a *App) removeMergedPartInputs(dir, asin string) error {
	files, err := a.FS.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		name := file.Name()
		if !isAudiblePartFile(name, asin) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".aax" && ext != ".aaxc" && ext != ".voucher" && !(ext == ".json" && strings.HasSuffix(strings.ToLower(name), "-chapters.json")) {
			continue
		}
		if err := a.FS.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

func isAudiblePartFile(name, asin string) bool {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if !strings.HasPrefix(base, asin) {
		return false
	}
	return len(base) == len(asin) || base[len(asin)] == '-' || base[len(asin)] == '_'
}

func escapeFFconcatPath(path string) string {
	return strings.ReplaceAll(path, "'", "'\\''")
}

func conversionOutputPath(inputPath string) string {
	dir := filepath.Dir(inputPath)
	stem := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	return filepath.Join(dir, stripAudibleQualitySuffix(stem)+".m4b")
}

func pendingConversionFiles(fs FileSystem, files []string) []string {
	pending := files[:0]
	for _, inputPath := range files {
		if !fileExistsFS(fs, conversionOutputPath(inputPath)) {
			pending = append(pending, inputPath)
		}
	}
	return pending
}

func stripAudibleQualitySuffix(stem string) string {
	chapterSuffix := ""
	if strings.HasSuffix(stem, "-chapters") {
		stem = strings.TrimSuffix(stem, "-chapters")
		chapterSuffix = "-chapters"
	}

	if idx := strings.LastIndex(stem, "-LC_"); idx >= 0 {
		return stem[:idx] + chapterSuffix
	}
	if idx := strings.LastIndex(stem, "-AAX_"); idx >= 0 {
		return stem[:idx] + chapterSuffix
	}

	return stem + chapterSuffix
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
		output, removable := completedOutputForIntermediate(path)
		if removable && fileExistsFS(a.FS, output) {
			if err := a.FS.Remove(path); err != nil {
				return fmt.Errorf("failed to remove %s: %w", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to clean media directory: %w", err)
	}
	return nil
}

func completedOutputForIntermediate(path string) (string, bool) {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".aax"), strings.HasSuffix(lower, ".aaxc"):
		return conversionOutputPath(path), true
	case strings.HasSuffix(lower, ".voucher"):
		return conversionOutputPath(strings.TrimSuffix(path, filepath.Ext(path)) + ".aaxc"), true
	case strings.HasSuffix(lower, "-chapters.json"):
		base := path[:len(path)-len("-chapters.json")]
		return conversionOutputPath(base + ".aaxc"), true
	default:
		return "", false
	}
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
