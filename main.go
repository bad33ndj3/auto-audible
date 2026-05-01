package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const downloadedAsinsPath = "downloaded_asins.json"

var activationBytesPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}$`)
var whitespacePattern = regexp.MustCompile(`\s+`)

func sanitizeFileName(name string) string {
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "",
		"?", "",
		"\"", "'",
		"<", "",
		">", "",
		"|", "-",
	)
	name = replacer.Replace(name)
	name = whitespacePattern.ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)
	return name
}

func formatPrefix(seq interface{}) string {
	if seq == nil {
		return ""
	}
	var n int
	switch v := seq.(type) {
	case float64:
		n = int(v)
	case string:
		fmt.Sscanf(v, "%d", &n)
	default:
		return ""
	}
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%02d - ", n)
}

func findFilesRecursive(root, ext string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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

type config struct {
	MediaDir    string
	Password    string
	Profile     string
	StatusTable bool
}

type libraryItem struct {
	ASIN           string      `json:"asin"`
	Title          string      `json:"title"`
	SeriesTitle    string      `json:"series_title"`
	SeriesSequence interface{} `json:"series_sequence"`
}

type mediaState struct {
	Ready     []string
	NeedsAAX  []string
	NeedsAAXC []string
}

type voucherFile struct {
	ContentLicense struct {
		ASIN            string `json:"asin"`
		ContentMetadata struct {
			ContentReference struct {
				ContentFormat string `json:"content_format"`
			} `json:"content_reference"`
		} `json:"content_metadata"`
		LicenseResponse struct {
			Key string `json:"key"`
			IV  string `json:"iv"`
		} `json:"license_response"`
	} `json:"content_license"`
}

type chapterFile struct {
	ContentMetadata struct {
		ContentReference struct {
			ASIN          string `json:"asin"`
			ContentFormat string `json:"content_format"`
		} `json:"content_reference"`
	} `json:"content_metadata"`
}

type bookMediaInfo struct {
	HasM4B  bool
	HasAAX  bool
	HasAAXC bool
}

func main() {
	command, cfg, err := parseArgs(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatalf("invalid arguments: %v", err)
	}

	if command == "download" || command == "convert" || command == "status" || command == "all" {
		if err := ensureAudibleConfigured(); err != nil {
			log.Fatal(err)
		}
	}

	switch command {
	case "download":
		if err := doDownload(cfg); err != nil {
			log.Fatalf("download failed: %v", err)
		}
		if err := printReadySummary(cfg.MediaDir); err != nil {
			log.Fatalf("ready summary failed: %v", err)
		}
	case "convert":
		if err := doConvert(cfg); err != nil {
			log.Fatalf("convert failed: %v", err)
		}
		if err := printReadySummary(cfg.MediaDir); err != nil {
			log.Fatalf("ready summary failed: %v", err)
		}
	case "clean":
		if err := doClean(cfg); err != nil {
			log.Fatalf("clean failed: %v", err)
		}
	case "status":
		if err := doStatus(cfg); err != nil {
			log.Fatalf("status failed: %v", err)
		}
	case "ready":
		if err := printReadySummary(cfg.MediaDir); err != nil {
			log.Fatalf("ready summary failed: %v", err)
		}
	case "all":
		if err := doDownload(cfg); err != nil {
			log.Fatalf("download failed: %v", err)
		}
		if err := doConvert(cfg); err != nil {
			log.Fatalf("convert failed: %v", err)
		}
		if err := printReadySummary(cfg.MediaDir); err != nil {
			log.Fatalf("ready summary failed: %v", err)
		}
	default:
		usage()
		os.Exit(1)
	}
}

func parseArgs(args []string) (string, config, error) {
	if len(args) == 0 {
		usage()
		return "", config{}, fmt.Errorf("missing command")
	}

	command := args[0]
	switch command {
	case "download", "convert", "clean", "ready", "status", "all":
	default:
		usage()
		return "", config{}, fmt.Errorf("unknown command %q", command)
	}

	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	cfg := config{}
	fs.StringVar(&cfg.MediaDir, "media-dir", "media", "directory for downloaded and converted media")
	fs.StringVar(&cfg.Password, "password", "", "optional audible auth-file password")
	fs.StringVar(&cfg.Profile, "profile", "", "optional audible-cli profile")
	fs.BoolVar(&cfg.StatusTable, "status-table", false, "for status command: show all books in a table")

	if err := fs.Parse(args[1:]); err != nil {
		return "", config{}, err
	}
	if fs.NArg() != 0 {
		return "", config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	return command, cfg, nil
}

func usage() {
	fmt.Println("Usage:")
	fmt.Println("  go run . <command> [flags]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  download   Export the Audible library and download new titles")
	fmt.Println("  convert    Convert downloaded .aax/.aaxc files to .m4b with ffmpeg")
	fmt.Println("  clean      Remove intermediate download files from the media dir")
	fmt.Println("  ready      List ready-to-listen and pending books")
	fmt.Println("  status     Show library and media status summary")
	fmt.Println("  all        Run download and convert")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --media-dir <dir>   Media directory (default: media)")
	fmt.Println("  --password <pwd>   Optional auth-file password")
	fmt.Println("  --profile <name>   Optional audible-cli profile")
	fmt.Println("  --status-table     For status command: show full table")
	fmt.Println()
	fmt.Println("If audible-cli is not configured yet, the program will ask to run `audible quickstart`.")
	fmt.Println("If --password is omitted, audible-cli will use an unencrypted auth file or prompt when needed.")
}

func ensureAudibleConfigured() error {
	if _, err := exec.LookPath("audible"); err != nil {
		return fmt.Errorf("audible-cli was not found on PATH")
	}

	hasProfiles, err := hasAudibleProfile()
	if err == nil && hasProfiles {
		return nil
	}

	if !isInteractive() {
		return fmt.Errorf("audible-cli does not appear configured; run `audible quickstart` once and rerun this command")
	}

	shouldRun, promptErr := promptYesNo("audible-cli does not appear configured. Run `audible quickstart` now? [y/N]: ", false)
	if promptErr != nil {
		return fmt.Errorf("failed to confirm audible quickstart state: %w", promptErr)
	}
	if !shouldRun {
		return fmt.Errorf("audible-cli is required; run `audible quickstart` and rerun this command")
	}

	if err := runCmd("audible", "quickstart"); err != nil {
		return fmt.Errorf("audible quickstart failed: %w", err)
	}

	hasProfiles, err = hasAudibleProfile()
	if err != nil {
		return fmt.Errorf("failed to validate audible quickstart: %w", err)
	}
	if !hasProfiles {
		return fmt.Errorf("audible quickstart finished, but no profile was detected")
	}

	return nil
}

func hasAudibleProfile() (bool, error) {
	output, err := runCmdOutput("audible", "manage", "profile", "list")
	if err != nil {
		return false, err
	}

	rowCount := 0
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") {
			rowCount++
		}
	}

	return rowCount >= 2, nil
}

func doDownload(cfg config) error {
	if err := os.MkdirAll(cfg.MediaDir, 0o755); err != nil {
		return fmt.Errorf("failed to create media directory: %w", err)
	}

	libraryFile, err := os.CreateTemp("", "auto-audible-library-*.json")
	if err != nil {
		return fmt.Errorf("failed to create temporary library export file: %w", err)
	}
	libraryPath := libraryFile.Name()
	if err := libraryFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary library export file: %w", err)
	}
	defer os.Remove(libraryPath)

	fmt.Println("Exporting library to temporary JSON file")
	if err := runCmd("audible", audibleArgs(cfg, "library", "export", "--format", "json", "--output", libraryPath)...); err != nil {
		return fmt.Errorf("failed to export library: %w", err)
	}

	items, err := parseLibraryItemsJSON(libraryPath)
	if err != nil {
		return fmt.Errorf("failed to parse library export: %w", err)
	}

	if len(items) == 0 {
		fmt.Println("No library items found in Audible export")
		return nil
	}

	downloaded, err := loadDownloadedAsins()
	if err != nil {
		return fmt.Errorf("failed to load downloaded ASINs: %w", err)
	}

	downloadedSet := make(map[string]struct{}, len(downloaded))
	for _, asin := range downloaded {
		downloadedSet[asin] = struct{}{}
	}

	for _, item := range items {
		asin := item.ASIN
		if _, ok := downloadedSet[asin]; ok {
			fmt.Printf("ASIN %s already downloaded, skipping.\n", asin)
			continue
		}

		outputDir := cfg.MediaDir
		seriesTitle := strings.TrimSpace(item.SeriesTitle)
		hasSeries := seriesTitle != ""
		if hasSeries {
			seriesDir := filepath.Join(cfg.MediaDir, sanitizeFileName(seriesTitle))
			if err := os.MkdirAll(seriesDir, 0o755); err != nil {
				return fmt.Errorf("failed to create series directory: %w", err)
			}
			outputDir = seriesDir
		}

		fmt.Printf("Downloading new book with ASIN: %s (%s)\n", asin, item.Title)
		err := runCmd(
			"audible",
			audibleArgs(
				cfg,
				"download",
				"--asin", asin,
				"--output-dir", outputDir,
				"--filename-mode", "asin_only",
				"-y",
				"--ignore-errors",
				"--aax-fallback",
				"--timeout", "5000",
				"--pdf",
				"--cover",
				"--chapter",
				"-q", "high",
				"--overwrite",
				"--ignore-podcasts",
			)...,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to download ASIN %s: %v\n", asin, err)
			continue
		}

		if err := renameDownloadedFiles(outputDir, asin, item.Title, hasSeries, item.SeriesSequence); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to rename files for ASIN %s: %v\n", asin, err)
		}

		downloaded = append(downloaded, asin)
		downloadedSet[asin] = struct{}{}
		if err := saveDownloadedAsins(downloaded); err != nil {
			return fmt.Errorf("failed to save downloaded ASINs: %w", err)
		}
	}

	return nil
}

func renameDownloadedFiles(outputDir, asin, title string, hasSeries bool, seriesSeq interface{}) error {
	files, err := os.ReadDir(outputDir)
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
		if fileExists(newPath) && name != newName {
			counter := 1
			for {
				altName := prefix + sanitizedTitle + fmt.Sprintf("_%d", counter) + ext
				altPath := filepath.Join(outputDir, altName)
				if !fileExists(altPath) {
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
		if err := os.Rename(oldPath, newPath); err != nil {
			return fmt.Errorf("failed to rename %s to %s: %w", name, newName, err)
		}
	}

	return nil
}

func doConvert(cfg config) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg was not found on PATH; install ffmpeg or use the Docker image for conversion")
	}

	aaxFiles, err := findFilesRecursive(cfg.MediaDir, ".aax")
	if err != nil {
		return fmt.Errorf("failed to list .aax files: %w", err)
	}
	aaxcFiles, err := findFilesRecursive(cfg.MediaDir, ".aaxc")
	if err != nil {
		return fmt.Errorf("failed to list .aaxc files: %w", err)
	}

	if len(aaxFiles) == 0 && len(aaxcFiles) == 0 {
		fmt.Println("No .aax or .aaxc files found to convert")
		return nil
	}

	activationBytes := ""
	if len(aaxFiles) > 0 {
		activationBytes, err = getActivationBytes(cfg)
		if err != nil {
			return err
		}
	}

	failed := 0
	for _, inputPath := range aaxFiles {
		outputPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".m4b"
		fmt.Printf("Converting %s -> %s\n", inputPath, outputPath)

		err := runCmd(
			"ffmpeg",
			"-y",
			"-loglevel", "error",
			"-stats",
			"-activation_bytes", activationBytes,
			"-i", inputPath,
			"-vn",
			"-c:a", "copy",
			outputPath,
		)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "Failed to convert %s: %v\n", inputPath, err)
		}
	}

	for _, inputPath := range aaxcFiles {
		outputPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".m4b"
		voucherPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".voucher"

		key, iv, keyErr := loadVoucherKeyIV(voucherPath)
		if keyErr != nil {
			failed++
			fmt.Fprintf(os.Stderr, "Failed to load voucher for %s: %v\n", inputPath, keyErr)
			continue
		}

		fmt.Printf("Converting %s -> %s\n", inputPath, outputPath)
		err := runCmd(
			"ffmpeg",
			"-y",
			"-loglevel", "error",
			"-stats",
			"-audible_key", key,
			"-audible_iv", iv,
			"-i", inputPath,
			"-vn",
			"-c:a", "copy",
			outputPath,
		)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "Failed to convert %s: %v\n", inputPath, err)
		}
	}

	if failed > 0 {
		return fmt.Errorf("failed to convert %d file(s)", failed)
	}

	return nil
}

func getActivationBytes(cfg config) (string, error) {
	output, err := runCmdOutput("audible", audibleArgs(cfg, "activation-bytes")...)
	if err != nil {
		return "", fmt.Errorf("failed to fetch activation bytes: %w", err)
	}

	activationBytes := extractActivationBytes(output)
	if activationBytes == "" {
		return "", fmt.Errorf("audible activation-bytes did not return a usable activation key")
	}

	return activationBytes, nil
}

func extractActivationBytes(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if activationBytesPattern.MatchString(line) {
			return strings.ToLower(line)
		}
	}
	return ""
}

func parseLibraryJSON(filename string) ([]string, error) {
	items, err := parseLibraryItemsJSON(filename)
	if err != nil {
		return nil, err
	}

	asins := make([]string, 0, len(items))
	for _, item := range items {
		asins = append(asins, item.ASIN)
	}

	return asins, nil
}

func parseLibraryItemsJSON(filename string) ([]libraryItem, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var items []libraryItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(items))
	filtered := make([]libraryItem, 0, len(items))
	for _, item := range items {
		asin := strings.TrimSpace(item.ASIN)
		if asin == "" {
			continue
		}
		if _, ok := seen[asin]; ok {
			continue
		}
		seen[asin] = struct{}{}
		item.ASIN = asin
		filtered = append(filtered, item)
	}

	return filtered, nil
}

func loadVoucherKeyIV(voucherPath string) (string, string, error) {
	data, err := os.ReadFile(voucherPath)
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

func doClean(cfg config) error {
	err := filepath.WalkDir(cfg.MediaDir, func(path string, d os.DirEntry, err error) error {
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
			if err := os.Remove(path); err != nil {
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

func doStatus(cfg config) error {
	items, err := exportLibraryItems(cfg)
	if err != nil {
		return err
	}

	downloaded, err := loadDownloadedAsins()
	if err != nil {
		return fmt.Errorf("failed to load downloaded ASINs: %w", err)
	}
	downloadedSet := make(map[string]struct{}, len(downloaded))
	for _, asin := range downloaded {
		downloadedSet[asin] = struct{}{}
	}

	state, err := scanMediaState(cfg.MediaDir)
	if err != nil {
		return err
	}
	asinMediaIndex, err := buildASINMediaIndex(cfg.MediaDir)
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

	if cfg.StatusTable {
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

func exportLibraryItems(cfg config) ([]libraryItem, error) {
	libraryFile, err := os.CreateTemp("", "auto-audible-library-*.json")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary library export file: %w", err)
	}
	libraryPath := libraryFile.Name()
	if err := libraryFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temporary library export file: %w", err)
	}
	defer os.Remove(libraryPath)

	if err := runCmd("audible", audibleArgs(cfg, "library", "export", "--format", "json", "--output", libraryPath)...); err != nil {
		return nil, fmt.Errorf("failed to export library: %w", err)
	}

	items, err := parseLibraryItemsJSON(libraryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse library export: %w", err)
	}

	return items, nil
}

func scanMediaState(mediaDir string) (mediaState, error) {
	hasM4B := map[string]struct{}{}
	hasAAX := map[string]struct{}{}
	hasAAXC := map[string]struct{}{}

	err := filepath.WalkDir(mediaDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(mediaDir, path)
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
			return mediaState{}, nil
		}
		return mediaState{}, fmt.Errorf("failed to read media directory: %w", err)
	}

	state := mediaState{}
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

func buildASINMediaIndex(mediaDir string) (map[string]bookMediaInfo, error) {
	index := map[string]bookMediaInfo{}

	err := filepath.WalkDir(mediaDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".voucher") {
			return nil
		}
		data, readErr := os.ReadFile(path)
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
			info = mergeMediaInfo(info, mediaInfoForBase(dir, candidate))
		}
		index[asin] = info
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list voucher files: %w", err)
	}

	err = filepath.WalkDir(mediaDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), "-chapters.json") {
			return nil
		}
		data, readErr := os.ReadFile(path)
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
			info = mergeMediaInfo(info, mediaInfoForBase(dir, candidate))
		}
		index[asin] = info
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list chapter metadata files: %w", err)
	}

	return index, nil
}

func mergeMediaInfo(a, b bookMediaInfo) bookMediaInfo {
	return bookMediaInfo{
		HasM4B:  a.HasM4B || b.HasM4B,
		HasAAX:  a.HasAAX || b.HasAAX,
		HasAAXC: a.HasAAXC || b.HasAAXC,
	}
}

func mediaInfoForBase(dir, base string) bookMediaInfo {
	return bookMediaInfo{
		HasM4B:  fileExists(filepath.Join(dir, base+".m4b")),
		HasAAX:  fileExists(filepath.Join(dir, base+".aax")),
		HasAAXC: fileExists(filepath.Join(dir, base+".aaxc")),
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func trimCodecSuffix(base, codec string) string {
	codec = strings.TrimSpace(codec)
	if codec == "" {
		return base
	}
	suffix := "-" + codec
	if strings.HasSuffix(base, suffix) {
		return strings.TrimSuffix(base, suffix)
	}
	return base
}

func computeBookState(tracked bool, info bookMediaInfo) string {
	if info.HasM4B {
		return "ready"
	}
	if info.HasAAX {
		return "needs_convert_aax"
	}
	if info.HasAAXC {
		return "needs_convert_aaxc"
	}
	if tracked {
		return "tracked_no_media"
	}
	return "not_downloaded"
}

func printStatusTable(rows [][3]string) {
	asinWidth := len("ASIN")
	titleWidth := len("Title")
	stateWidth := len("State")
	for _, row := range rows {
		if len(row[0]) > asinWidth {
			asinWidth = len(row[0])
		}
		if len(row[1]) > titleWidth {
			titleWidth = len(row[1])
		}
		if len(row[2]) > stateWidth {
			stateWidth = len(row[2])
		}
	}

	fmt.Printf("%-*s  %-*s  %-*s\n", asinWidth, "ASIN", titleWidth, "Title", stateWidth, "State")
	for _, row := range rows {
		fmt.Printf("%-*s  %-*s  %-*s\n", asinWidth, row[0], titleWidth, row[1], stateWidth, row[2])
	}
}

func printReadySummary(mediaDir string) error {
	state, err := scanMediaState(mediaDir)
	if err != nil {
		return err
	}

	fmt.Printf("\nReady-to-listen summary for %s\n", mediaDir)
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

func loadDownloadedAsins() ([]string, error) {
	if _, err := os.Stat(downloadedAsinsPath); os.IsNotExist(err) {
		return []string{}, nil
	}

	data, err := os.ReadFile(downloadedAsinsPath)
	if err != nil {
		return nil, err
	}

	var asins []string
	if err := json.Unmarshal(data, &asins); err != nil {
		return nil, err
	}

	return asins, nil
}

func saveDownloadedAsins(asins []string) error {
	data, err := json.MarshalIndent(asins, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(downloadedAsinsPath, data, 0o644)
}

func audibleArgs(cfg config, args ...string) []string {
	cmdArgs := make([]string, 0, len(args)+4)
	if cfg.Profile != "" {
		cmdArgs = append(cmdArgs, "--profile", cfg.Profile)
	}
	if cfg.Password != "" {
		cmdArgs = append(cmdArgs, "--password", cfg.Password)
	}
	cmdArgs = append(cmdArgs, args...)
	return cmdArgs
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runCmdOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, trimmed)
	}
	return trimmed, nil
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

func promptYesNo(prompt string, defaultYes bool) (bool, error) {
	fmt.Print(prompt)

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}

	answer := strings.ToLower(strings.TrimSpace(line))
	switch answer {
	case "", "y", "yes":
		return answer != "" || defaultYes, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("expected yes or no")
	}
}
