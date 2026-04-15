# AGENTS.md

## Purpose

This repository contains a small Go wrapper around `audible-cli`. The wrapper is responsible for:

- checking whether `audible-cli` is installed and configured
- exporting the Audible library as JSON
- downloading only new ASINs
- converting supported `.aax` downloads to `.m4b` with `ffmpeg`
- cleaning intermediate files from the media directory

## Important behavior

- `--password` is optional by design
- missing Audible setup should be detected through `audible manage profile list`
- interactive runs may offer to launch `audible quickstart`
- conversion currently supports `.aax` only; `.aaxc` should be reported, not silently treated as converted
- `decrypt` is an alias for `convert` to preserve older usage

## Files that matter

- `main.go`: the full CLI implementation
- `main_test.go`: unit tests for argument and parsing helpers
- `Taskfile.yaml`: local shortcuts
- `Dockerfile`: image with Go binary, `audible-cli`, and `ffmpeg`
- `README.md`: user-facing setup and usage

## Commands

```bash
go test ./...
go build ./...
go run . download
go run . convert
go run . all
task all
```

## Editing guidance

- keep the CLI small and stdlib-first unless there is a clear reason not to
- prefer repairing the wrapper around current `audible-cli` behavior instead of reintroducing old assumptions
- preserve user data in `media/` and `downloaded_asins.json`
- avoid changes that trigger large audiobook downloads during verification
