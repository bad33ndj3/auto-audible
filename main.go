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
	"strings"
)

const downloadedAsinsPath = "downloaded_asins.json"

var activationBytesPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}$`)

type config struct {
	MediaDir string
	Password string
	Profile  string
}

type libraryItem struct {
	ASIN string `json:"asin"`
}

func main() {
	command, cfg, err := parseArgs(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatalf("invalid arguments: %v", err)
	}

	if command != "clean" {
		if err := ensureAudibleConfigured(); err != nil {
			log.Fatal(err)
		}
	}

	switch command {
	case "download":
		if err := doDownload(cfg); err != nil {
			log.Fatalf("download failed: %v", err)
		}
	case "convert", "decrypt":
		if err := doConvert(cfg); err != nil {
			log.Fatalf("convert failed: %v", err)
		}
	case "clean":
		if err := doClean(cfg); err != nil {
			log.Fatalf("clean failed: %v", err)
		}
	case "all":
		if err := doDownload(cfg); err != nil {
			log.Fatalf("download failed: %v", err)
		}
		if err := doConvert(cfg); err != nil {
			log.Fatalf("convert failed: %v", err)
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
	case "download", "convert", "decrypt", "clean", "all":
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
	fmt.Println("  convert    Convert downloaded .aax files to .m4b with ffmpeg")
	fmt.Println("  decrypt    Alias for convert (kept for older usage)")
	fmt.Println("  clean      Remove intermediate download files from the media dir")
	fmt.Println("  all        Run download and convert")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --media-dir <dir>   Media directory (default: media)")
	fmt.Println("  --password <pwd>   Optional auth-file password")
	fmt.Println("  --profile <name>   Optional audible-cli profile")
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

	asins, err := parseLibraryJSON(libraryPath)
	if err != nil {
		return fmt.Errorf("failed to parse library export: %w", err)
	}

	if len(asins) == 0 {
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

	for _, asin := range asins {
		if _, ok := downloadedSet[asin]; ok {
			fmt.Printf("ASIN %s already downloaded, skipping.\n", asin)
			continue
		}

		fmt.Printf("Downloading new book with ASIN: %s\n", asin)
		err := runCmd(
			"audible",
			audibleArgs(
				cfg,
				"download",
				"--asin", asin,
				"--output-dir", cfg.MediaDir,
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

		downloaded = append(downloaded, asin)
		downloadedSet[asin] = struct{}{}
		if err := saveDownloadedAsins(downloaded); err != nil {
			return fmt.Errorf("failed to save downloaded ASINs: %w", err)
		}
	}

	return nil
}

func doConvert(cfg config) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg was not found on PATH; install ffmpeg or use the Docker image for conversion")
	}

	activationBytes, err := getActivationBytes(cfg)
	if err != nil {
		return err
	}

	aaxFiles, err := filepath.Glob(filepath.Join(cfg.MediaDir, "*.aax"))
	if err != nil {
		return fmt.Errorf("failed to list .aax files: %w", err)
	}
	aaxcFiles, err := filepath.Glob(filepath.Join(cfg.MediaDir, "*.aaxc"))
	if err != nil {
		return fmt.Errorf("failed to list .aaxc files: %w", err)
	}

	if len(aaxFiles) == 0 {
		fmt.Println("No .aax files found to convert")
		if len(aaxcFiles) > 0 {
			fmt.Fprintf(os.Stderr, "Found %d .aaxc file(s); automatic .m4b conversion currently only supports .aax files.\n", len(aaxcFiles))
		}
		return nil
	}

	failed := 0
	for _, inputPath := range aaxFiles {
		outputPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".m4b"
		fmt.Printf("Converting %s -> %s\n", filepath.Base(inputPath), filepath.Base(outputPath))

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
			fmt.Fprintf(os.Stderr, "Failed to convert %s: %v\n", filepath.Base(inputPath), err)
		}
	}

	if len(aaxcFiles) > 0 {
		fmt.Fprintf(os.Stderr, "Found %d .aaxc file(s); automatic .m4b conversion currently only supports .aax files.\n", len(aaxcFiles))
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
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var items []libraryItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(items))
	var asins []string
	for _, item := range items {
		asin := strings.TrimSpace(item.ASIN)
		if asin == "" {
			continue
		}
		if _, ok := seen[asin]; ok {
			continue
		}
		seen[asin] = struct{}{}
		asins = append(asins, asin)
	}

	return asins, nil
}

func doClean(cfg config) error {
	files, err := os.ReadDir(cfg.MediaDir)
	if err != nil {
		return fmt.Errorf("failed to read media directory: %w", err)
	}

	for _, file := range files {
		name := file.Name()
		if strings.HasSuffix(name, ".aaxc") ||
			strings.HasSuffix(name, ".aax") ||
			strings.HasSuffix(name, ".jpg") ||
			strings.HasSuffix(name, ".json") ||
			strings.HasSuffix(name, ".voucher") ||
			strings.HasSuffix(name, ".pdf") {
			if err := os.Remove(filepath.Join(cfg.MediaDir, name)); err != nil {
				return fmt.Errorf("failed to remove %s: %w", name, err)
			}
		}
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
