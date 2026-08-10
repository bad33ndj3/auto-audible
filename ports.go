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
	// GetAudioParts returns the ordered part ASINs for a book that Audible
	// splits into multiple AudioPart items. Returns an empty slice for
	// normal, single-file books.
	GetAudioParts(ctx context.Context, asin string) ([]string, error)
}

// MediaConverter abstracts ffmpeg conversions.
type MediaConverter interface {
	ConvertAAX(ctx context.Context, inputPath, outputPath, activationBytes string) error
	ConvertAAXC(ctx context.Context, inputPath, outputPath, key, iv string) error
	// ConcatM4B losslessly concatenates ordered .m4b part files into a
	// single output file.
	ConcatM4B(ctx context.Context, listPath, outputPath string) error
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
