# AGENTS.md

## Purpose

This repository contains a small Go wrapper around `audible-cli`. The wrapper is responsible for:

- checking whether `audible-cli` is installed and configured
- exporting the Audible library as JSON
- downloading only new ASINs
- organising series books into subfolders with a `## - ` prefix
- converting supported `.aax` downloads to `.m4b` with `ffmpeg`
- cleaning intermediate files from the media directory

## Important behavior

- `--password` is optional by design
- missing Audible setup should be detected through `audible manage profile list`
- interactive runs may offer to launch `audible quickstart`
- conversion supports `.aax` (activation bytes) and `.aaxc` (voucher key/iv)
- series books are placed in `media/<Series>/` and renamed with a `## - ` prefix
- all media operations (`convert`, `clean`, `status`, `ready`) are recursive

## Files that matter

- `main.go`: CLI entrypoint — parsing, wiring adapters, dispatching commands
- `domain.go`: pure business logic — entities, value objects, stateless helpers
- `ports.go`: interfaces for all external dependencies (audible-cli, ffmpeg, filesystem, prompter, store)
- `adapters.go`: live infrastructure adapters that implement the ports
- `app.go`: application service — orchestrates use cases (`Download`, `Convert`, `Clean`, `Status`)
- `main_test.go`: unit tests for domain logic and CLI parsing
- `app_test.go`: TDD-style application tests using fake adapters
- `Taskfile.yaml`: local shortcuts
- `Dockerfile`: image with Go binary, `audible-cli`, and `ffmpeg`
- `README.md`: user-facing setup and usage

## Commands

```bash
go test ./...
go build ./...
go run . download
go run . convert
go run . status
go run . all
task all
```

## Editing guidance

- keep the CLI small and stdlib-first unless there is a clear reason not to
- prefer repairing the wrapper around current `audible-cli` behavior instead of reintroducing old assumptions
- preserve user data in `media/` and `downloaded_asins.json`
- avoid changes that trigger large audiobook downloads during verification
