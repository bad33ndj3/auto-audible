# Auto Audible

`auto-audible` wraps [`audible-cli`](https://github.com/mkb79/audible-cli) so you can export your library, download new titles, convert supported downloads to `.m4b`, and clean up the intermediate files.

## What changed

- `--password` is now optional.
- If your Audible auth file is unencrypted, you can omit `--password` entirely.
- If your auth file is encrypted, `audible-cli` can prompt for the password when needed.
- The tool now detects whether `audible-cli` has been configured already and, when run interactively, offers to start `audible quickstart`.
- Library export now uses JSON instead of TSV, which is less brittle against upstream format changes.
- The old `audible decrypt` step was replaced with `.aax -> .m4b` conversion through `ffmpeg` plus `audible activation-bytes`.

## Requirements

For running on the host:

- Go 1.23+
- `audible-cli`
- `ffmpeg`
- A configured Audible account

Run the one-time Audible setup first if you have not done it yet:

```bash
audible quickstart
```

The wrapper will also detect missing setup and ask to run it when you start the program interactively.

## Commands

```bash
go run . sync     [--profile audible] [--password secret] [--media-dir media]
go run . plan     [--profile audible] [--password secret] [--media-dir media]
go run . download [--profile audible] [--password secret] [--media-dir media]
go run . convert  [--profile audible] [--password secret] [--media-dir media]
go run . clean    [--media-dir media]
go run . ready    [--media-dir media]
go run . status   [--profile audible] [--password secret] [--media-dir media]
go run . status   --table [--profile audible] [--password secret] [--media-dir media]
```

Command summary:

- `sync`: the normal command: download, convert, safely remove completed conversion inputs, and print what is ready
- `plan`: lists the exact titles the next `sync` will skip and download; it never writes media
- `all`: deprecated alias for `sync`, retained for existing scripts
- `download`: exports the Audible library and downloads missing books; tracked books are retried if their media vanished
- `convert`: converts `.aax` and `.aaxc` files in the media directory to `.m4b`
- `clean`: removes only `.aax`, `.aaxc`, `.voucher`, and `-chapters.json` files that already have a committed `.m4b`; it keeps covers, PDFs, unrelated JSON, and anything still needed for recovery
- `ready`: lists ready-to-listen `.m4b` titles and what is still pending (`.aax` / `.aaxc`)
- `status`: checks current library/media status (total library, tracked downloads, remaining, ready, pending conversions)
Downloads are deliberately serial. Audible is the bottleneck, and a single ordered flow keeps the media ledger safe and predictable.

## Auth behavior

Encrypted auth file:

```bash
go run . sync --password "your-auth-password"
```

Unencrypted auth file:

```bash
go run . sync
```

Profile-specific usage:

```bash
go run . download --profile audible
```

## How it works

1. `auto-audible` checks whether `audible-cli` is available and configured.
2. It exports your Audible library as JSON.
3. It tracks completed downloads in `<media-dir>/.auto-audible.json` (and imports the old `downloaded_asins.json` once for the default `media/` directory).
4. Books that belong to a series are placed in a subfolder named after the series.
5. Series books are renamed with a `## - ` prefix (e.g. `15 - Armor World.m4b`).
6. It asks `audible-cli` for activation bytes (for `.aax` conversion).
7. It uses `ffmpeg` to convert `.aax` and `.aaxc` files to `.m4b`.

## Series folders

When `download` detects a `series_title` in your Audible library, it creates a folder for that series under `media/` and downloads the book there. Files are renamed after download so they sort naturally:

```
media/
├── Undying Mercenaries/
│   ├── 01 - Steel World.m4b
│   ├── 02 - Dust World.m4b
│   └── 15 - Armor World.m4b
├── Bobiverse/
│   ├── 01 - We Are Legion (We Are Bob).m4b
│   └── 02 - For We Are Many.m4b
├── Project Hail Mary.m4b
└── The Phoenix Project.m4b
```

Books without a series are kept at the top level of `media/`. All commands (`convert`, `clean`, `ready`, `status`) work recursively across the entire `media/` tree.

## AAXC notes

For `.aaxc`, conversion requires the matching `.voucher` file (same basename) because the key and IV are read from that voucher.

## Taskfile shortcuts

```bash
task
task sync
task plan
task download
task convert
task clean
task ready
task status
task docker-sync
task docker-build
task all          # deprecated alias for sync
task docker-all   # deprecated alias for docker-sync
```

`status --table` lists every library title with one of these states:

- `ready`
- `needs_convert_aax`
- `needs_convert_aaxc`
- `tracked_no_media`
- `not_downloaded`

## Docker

The repository now includes a Docker image that bundles:

- `auto-audible`
- `audible-cli`
- `ffmpeg`

Build it:

```bash
docker build -t auto-audible .
```

Run it against your existing Audible config and the current repository directory:

```bash
docker run --rm -it \
  -v "$HOME/.audible:/root/.audible" \
  -v "$PWD:/work" \
  -w /work \
  auto-audible sync
```

Use a specific profile:

```bash
docker run --rm -it \
  -v "$HOME/.audible:/root/.audible" \
  -v "$PWD:/work" \
  -w /work \
  auto-audible download --profile audible
```

Use an encrypted auth file:

```bash
docker run --rm -it \
  -v "$HOME/.audible:/root/.audible" \
  -v "$PWD:/work" \
  -w /work \
  auto-audible sync --password "your-auth-password"
```

If you prefer a different config directory, mount it and set `AUDIBLE_CONFIG_DIR`.

## Verification

The Go code is covered by unit tests and was verified against the installed `audible-cli` setup for:

- optional password handling
- Audible profile detection
- library export through the wrapper without triggering new audiobook downloads

## Development

```bash
go test -race -cover ./...
go vet ./...
go build ./...
```

GitHub Actions runs the race-enabled tests and `go vet` for pushes and pull requests. Dependabot checks Go modules and GitHub Actions weekly. Devbox is not supported by Dependabot; refresh `devbox.lock` manually with `devbox update`.

## License

MIT. See [LICENSE](LICENSE).
