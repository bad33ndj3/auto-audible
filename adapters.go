package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// voucherFile represents the JSON structure of a .voucher file.
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

// chapterFile represents the JSON structure of a -chapters.json file.
type chapterFile struct {
	ContentMetadata struct {
		ContentReference struct {
			ASIN          string `json:"asin"`
			ContentFormat string `json:"content_format"`
		} `json:"content_reference"`
	} `json:"content_metadata"`
}

// audibleCLI is the live adapter for audible-cli.
type audibleCLI struct {
	profile  string
	password string
}

func newAudibleCLI(profile, password string) *audibleCLI {
	return &audibleCLI{profile: profile, password: password}
}

func (a *audibleCLI) args(base ...string) []string {
	cmdArgs := make([]string, 0, len(base)+4)
	if a.profile != "" {
		cmdArgs = append(cmdArgs, "--profile", a.profile)
	}
	if a.password != "" {
		cmdArgs = append(cmdArgs, "--password", a.password)
	}
	cmdArgs = append(cmdArgs, base...)
	return cmdArgs
}

func (a *audibleCLI) HasProfile(ctx context.Context) (bool, error) {
	output, err := a.runOutput(ctx, "manage", "profile", "list")
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

func (a *audibleCLI) RunQuickstart(ctx context.Context) error {
	return a.run(ctx, "quickstart")
}

func (a *audibleCLI) ExportLibrary(ctx context.Context) ([]Book, error) {
	libraryFile, err := os.CreateTemp("", "auto-audible-library-*.json")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary library export file: %w", err)
	}
	libraryPath := libraryFile.Name()
	if err := libraryFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temporary library export file: %w", err)
	}
	defer os.Remove(libraryPath)

	if err := a.run(ctx, "library", "export", "--format", "json", "--output", libraryPath); err != nil {
		return nil, fmt.Errorf("failed to export library: %w", err)
	}

	items, err := parseLibraryItemsJSON(&osFS{}, libraryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse library export: %w", err)
	}

	return items, nil
}

func (a *audibleCLI) DownloadBook(ctx context.Context, asin, outputDir string) error {
	return a.run(ctx,
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
	)
}

// audioPartsResponse is the shape of `audible api library` responses.
type audioPartsResponse struct {
	Items []struct {
		ASIN string `json:"asin"`
	} `json:"items"`
}

func (a *audibleCLI) GetAudioParts(ctx context.Context, asin string) ([]string, error) {
	output, err := a.runOutput(ctx,
		"api", "library",
		"-p", "parent_asin="+asin,
		"-p", "response_groups=product_attrs",
		"-f", "json",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch audio parts for %s: %w", asin, err)
	}

	var resp audioPartsResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse audio parts response for %s: %w", asin, err)
	}

	parts := make([]string, 0, len(resp.Items))
	for _, item := range resp.Items {
		if asin := strings.TrimSpace(item.ASIN); asin != "" {
			if !validASIN(asin) {
				return nil, fmt.Errorf("audio parts response contains invalid ASIN %q", asin)
			}
			parts = append(parts, asin)
		}
	}
	return parts, nil
}

func (a *audibleCLI) GetActivationBytes(ctx context.Context) (string, error) {
	output, err := a.runOutput(ctx, "activation-bytes")
	if err != nil {
		return "", fmt.Errorf("failed to fetch activation bytes: %w", err)
	}
	activationBytes := extractActivationBytes(output)
	if activationBytes == "" {
		return "", fmt.Errorf("audible activation-bytes did not return a usable activation key")
	}
	return activationBytes, nil
}

func (a *audibleCLI) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "audible", a.args(args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (a *audibleCLI) runOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "audible", a.args(args...)...)
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

// ffmpegConverter is the live adapter for ffmpeg.
type ffmpegConverter struct{}

func newFFmpegConverter() *ffmpegConverter {
	return &ffmpegConverter{}
}

func (f *ffmpegConverter) ConvertAAX(ctx context.Context, inputPath, outputPath, activationBytes string) error {
	return f.run(ctx,
		"-y",
		"-loglevel", "error",
		"-stats",
		"-activation_bytes", activationBytes,
		"-i", inputPath,
		"-vn",
		"-c:a", "copy",
		outputPath,
	)
}

func (f *ffmpegConverter) ConvertAAXC(ctx context.Context, inputPath, outputPath, key, iv string) error {
	return f.run(ctx,
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
}

func (f *ffmpegConverter) ConcatM4B(ctx context.Context, listPath, outputPath string) error {
	return f.run(ctx,
		"-y",
		"-loglevel", "error",
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-c", "copy",
		outputPath,
	)
}

func (f *ffmpegConverter) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// osFS is the live FileSystem adapter.
type osFS struct{}

func newOSFS() *osFS { return &osFS{} }

func (o *osFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (o *osFS) ReadDir(name string) ([]os.DirEntry, error)   { return os.ReadDir(name) }
func (o *osFS) ReadFile(name string) ([]byte, error)         { return os.ReadFile(name) }
func (o *osFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (o *osFS) Rename(oldpath, newpath string) error         { return os.Rename(oldpath, newpath) }
func (o *osFS) Remove(name string) error                     { return os.Remove(name) }
func (o *osFS) Stat(name string) (os.FileInfo, error)        { return os.Stat(name) }
func (o *osFS) WalkDir(root string, fn fs.WalkDirFunc) error { return filepath.WalkDir(root, fn) }

// jsonASINStore persists downloaded ASINs to a JSON file.
type jsonASINStore struct {
	path string
}

func newJSONASINStore(path string) *jsonASINStore {
	return &jsonASINStore{path: path}
}

func (s *jsonASINStore) Load() ([]string, error) {
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		return []string{}, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var asins []string
	if err := json.Unmarshal(data, &asins); err != nil {
		return nil, err
	}
	return asins, nil
}

func (s *jsonASINStore) Save(asins []string) error {
	data, err := json.MarshalIndent(asins, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// stdinPrompter reads yes/no answers from stdin.
type stdinPrompter struct{}

func newStdinPrompter() *stdinPrompter { return &stdinPrompter{} }

func (p *stdinPrompter) PromptYesNo(prompt string, defaultYes bool) (bool, error) {
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
