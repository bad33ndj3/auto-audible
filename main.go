package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
)

const downloadedAsinsPath = "downloaded_asins.json"

type config struct {
	MediaDir    string
	Password    string
	Profile     string
	StatusTable bool
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		log.Fatalf("error: %v", err)
	}
}

func run(ctx context.Context, args []string) error {
	cmd, cfg, err := parseArgs(args)
	if err != nil {
		return err
	}

	app := &App{
		Audible:   newAudibleCLI(cfg.Profile, cfg.Password),
		Converter: newFFmpegConverter(),
		FS:        newOSFS(),
		Store:     newJSONASINStore(downloadedAsinsPath),
		Prompter:  newStdinPrompter(),
		MediaDir:  cfg.MediaDir,
	}

	if cmd == "download" || cmd == "convert" || cmd == "status" || cmd == "all" {
		if err := app.EnsureAudibleConfigured(ctx); err != nil {
			return err
		}
	}

	switch cmd {
	case "download":
		if err := app.Download(ctx); err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
		if err := app.ReadySummary(); err != nil {
			return fmt.Errorf("ready summary failed: %w", err)
		}
	case "convert":
		if err := app.Convert(ctx); err != nil {
			return fmt.Errorf("convert failed: %w", err)
		}
		if err := app.ReadySummary(); err != nil {
			return fmt.Errorf("ready summary failed: %w", err)
		}
	case "clean":
		if err := app.Clean(ctx); err != nil {
			return fmt.Errorf("clean failed: %w", err)
		}
	case "status":
		if err := app.Status(ctx, cfg.StatusTable); err != nil {
			return fmt.Errorf("status failed: %w", err)
		}
	case "ready":
		if err := app.ReadySummary(); err != nil {
			return fmt.Errorf("ready summary failed: %w", err)
		}
	case "all":
		if err := app.Download(ctx); err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
		if err := app.Convert(ctx); err != nil {
			return fmt.Errorf("convert failed: %w", err)
		}
		if err := app.ReadySummary(); err != nil {
			return fmt.Errorf("ready summary failed: %w", err)
		}
	}

	return nil
}

func parseArgs(args []string) (string, config, error) {
	if len(args) == 0 {
		return "", config{}, fmt.Errorf("no command specified\n\nUsage: go run . <command> [flags]\n\nCommands:\n  download   Export library and download new titles\n  convert    Convert .aax/.aaxc files to .m4b\n  clean      Remove intermediate files\n  status     Show library and media status\n  ready      List ready-to-listen books\n  all        Run download and convert\n")
	}

	command := args[0]
	switch command {
	case "download", "convert", "clean", "ready", "status", "all":
	default:
		return "", config{}, fmt.Errorf("unknown command %q\n\nUsage: go run . <command> [flags]\n\nCommands: download, convert, clean, status, ready, all", command)
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
		return "", config{}, fmt.Errorf("unexpected arguments: %s", fs.Args())
	}

	return command, cfg, nil
}
