package main

import (
	"context"
	"io/fs"
	"os"
)

// AudibleService abstracts all interactions with audible-cli.
type AudibleService interface {
	HasProfile(ctx context.Context) (bool, error)
	RunQuickstart(ctx context.Context) error
	ExportLibrary(ctx context.Context) ([]Book, error)
	DownloadBook(ctx context.Context, asin, outputDir string) error
	GetActivationBytes(ctx context.Context) (string, error)
}

// MediaConverter abstracts ffmpeg conversions.
type MediaConverter interface {
	ConvertAAX(ctx context.Context, inputPath, outputPath, activationBytes string) error
	ConvertAAXC(ctx context.Context, inputPath, outputPath, key, iv string) error
}

// FileSystem abstracts OS file operations for testability.
type FileSystem interface {
	MkdirAll(path string, perm os.FileMode) error
	ReadDir(name string) ([]os.DirEntry, error)
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
	Rename(oldpath, newpath string) error
	Remove(name string) error
	Stat(name string) (os.FileInfo, error)
	WalkDir(root string, fn fs.WalkDirFunc) error
	CreateTemp(dir, pattern string) (*os.File, error)
}

// Prompter abstracts interactive user prompts.
type Prompter interface {
	PromptYesNo(prompt string, defaultYes bool) (bool, error)
}

// ASINStore persists the list of already-downloaded ASINs.
type ASINStore interface {
	Load() ([]string, error)
	Save(asins []string) error
}

// MediaIndexStore persists the media index (ASIN -> file location mapping).
type MediaIndexStore interface {
	Load() (MediaIndex, error)
	Save(index MediaIndex) error
}
