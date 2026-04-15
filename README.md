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
go run . download [--profile audible] [--password secret] [--media-dir media]
go run . convert  [--profile audible] [--password secret] [--media-dir media]
go run . clean    [--media-dir media]
go run . ready    [--media-dir media]
go run . status   [--profile audible] [--password secret] [--media-dir media]
go run . status   --status-table [--profile audible] [--password secret] [--media-dir media]
go run . all      [--profile audible] [--password secret] [--media-dir media]
```

Command summary:

- `download`: exports the Audible library and downloads books whose ASIN is not yet present in `downloaded_asins.json`
- `convert`: converts `.aax` and `.aaxc` files in the media directory to `.m4b`
- `clean`: removes intermediate `.aax`, `.aaxc`, `.jpg`, `.json`, `.voucher`, and `.pdf` files from the media directory
- `ready`: lists ready-to-listen `.m4b` titles and what is still pending (`.aax` / `.aaxc`)
- `status`: checks current library/media status (total library, tracked downloads, remaining, ready, pending conversions)
- `all`: runs `download` and `convert`

## Auth behavior

Encrypted auth file:

```bash
go run . all --password "your-auth-password"
```

Unencrypted auth file:

```bash
go run . all
```

Profile-specific usage:

```bash
go run . download --profile audible
```

## How it works

1. `auto-audible` checks whether `audible-cli` is available and configured.
2. It exports your Audible library as JSON.
3. It downloads only ASINs that are not already listed in `downloaded_asins.json`.
4. It asks `audible-cli` for activation bytes (for `.aax` conversion).
5. It uses `ffmpeg` to convert `.aax` and `.aaxc` files to `.m4b`.

## AAXC notes

For `.aaxc`, conversion requires the matching `.voucher` file (same basename) because the key and IV are read from that voucher.

## Taskfile shortcuts

```bash
task
task download
task convert
task clean
task ready
task status
task all
task docker-build
task docker-all
```

`status --status-table` lists every library title with one of these states:

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
  auto-audible all
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
  auto-audible all --password "your-auth-password"
```

If you prefer a different config directory, mount it and set `AUDIBLE_CONFIG_DIR`.

## Verification

The Go code is covered by unit tests and was verified against the installed `audible-cli` setup for:

- optional password handling
- Audible profile detection
- library export through the wrapper without triggering new audiobook downloads

## Development

```bash
go test ./...
go build ./...
```
