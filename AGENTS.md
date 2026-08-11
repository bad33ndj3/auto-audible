# AGENTS.md

## Purpose

This repository contains a small Go wrapper around `audible-cli`. The wrapper is responsible for:

- checking whether `audible-cli` is installed and configured
- exporting the Audible library as JSON
- downloading only new or missing ASINs (tracked ASINs are retried if their media vanished)
- organising series books into subfolders with a `## - ` prefix
- converting supported `.aax`/`.aaxc` downloads to `.m4b` with `ffmpeg`, merging multi-part books back into one file
- cleaning intermediate files from the media directory once they're safely superseded

`sync` (download → convert → clean → ready summary) is the primary command; `all` is a deprecated alias kept for old scripts.

## Important behavior

- `--password` is optional by design
- missing Audible setup is detected by running `audible manage profile list` and checking for a data row
- interactive runs may offer to launch `audible quickstart`
- conversion supports `.aax` (activation bytes) and `.aaxc` (voucher key/iv, read from the matching `.voucher` file)
- series books are placed in `media/<Series>/` and renamed with a `## - ` prefix
- all media operations (`convert`, `clean`, `status`, `ready`) are recursive across the whole media directory
- `plan` reports what the next `sync` will skip/download without writing any media
- downloaded-ASIN tracking lives in `<media-dir>/.auto-audible.json`; for the default `media/` dir it imports the legacy `downloaded_asins.json` once if the new file doesn't exist yet
- Audible sometimes splits a title into part ASINs; `download` records them in a `<asin>-parts.json` manifest, and once every part has converted to `.m4b`, `convert` losslessly concatenates them with ffmpeg and removes the parts/manifest
- ASINs are validated before ever being used in a filesystem path (rejects anything but `[A-Za-z0-9]+`)
- conversions write to a `.partial` sibling file first and only rename it onto the real output on success, so an interrupted ffmpeg run never leaves a file that looks converted
- `.aax`/`.aaxc` source files are deleted immediately after a successful conversion, not deferred until `clean`
- if a retried download re-produces sidecar files (`.jpg`, `.jpeg`, `-chapters.json`, `.voucher`) byte-identical to ones already renamed from a prior attempt, the duplicates are silently discarded; a rename conflict with *different* content still errors

## Files that matter

- `main.go`: CLI entrypoint — command table, flag parsing, `sync` orchestration
- `domain.go`: pure business logic — entities, value objects, stateless helpers
- `ports.go`: interfaces for all external dependencies (audible-cli, ffmpeg, filesystem, prompter, store)
- `adapters.go`: live infrastructure adapters that implement the ports
- `app.go`: application service — `Download`, `Convert`, `Clean`, `Status`, part-merging, retry/sidecar recovery
- `main_test.go` / `app_test.go`: CLI-parsing and TDD-style app tests (the latter using fake adapters)

## Commands

```bash
go test ./...
go build ./...
go run . sync      # download, convert, clean, print ready summary
go run . plan      # preview only, no writes
task all           # deprecated alias for `task sync`
```

## Editing guidance

- keep the CLI small and stdlib-first unless there is a clear reason not to
- prefer repairing the wrapper around current `audible-cli` behavior instead of reintroducing old assumptions
- preserve user data in `media/` and the ASIN store (`<media-dir>/.auto-audible.json`, plus its legacy `downloaded_asins.json` import path)
- avoid changes that trigger large audiobook downloads during verification
