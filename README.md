# Auto Audible

A wrapper around [`audible-cli`](https://github.com/mkb79/audible-cli) that exports your library, downloads new titles, converts `.aax`/`.aaxc` files to `.m4b`, and cleans up the leftovers.

## Requirements

- Go 1.23+
- `audible-cli`
- `ffmpeg`
- A configured Audible account

If you haven't set up `audible-cli` yet, run:

```bash
audible quickstart
```

If you skip this, `auto-audible` will notice when run interactively and offer to do it for you.

`--password` is only needed if your Audible auth file is encrypted. Unencrypted auth files work with no password flag at all.

## Commands

```bash
go run . sync     [--profile audible] [--password secret] [--media-dir media]
go run . plan     [--profile audible] [--password secret] [--media-dir media]
go run . download [--profile audible] [--password secret] [--media-dir media]
go run . convert  [--profile audible] [--password secret] [--media-dir media]
go run . clean    [--media-dir media]
go run . offload  --all [--yes] [--profile audible] [--password secret] [--media-dir media]
go run . ready    [--media-dir media]
go run . status   [--profile audible] [--password secret] [--media-dir media]
go run . status   --table [--profile audible] [--password secret] [--media-dir media]
```

`sync` is the one you actually want day to day: it downloads, converts, removes conversion inputs that are no longer needed, and prints what's ready to listen to. `plan` does the same lookup but only prints what `sync` would download and skip — it never touches disk. Use it to check before committing to a run. `all` is a deprecated alias for `sync`, kept around for old scripts.

After Audiobookshelf has imported your ready books, run `offload --all` to preview the exact `.m4b` files and Audible titles. Run it again with `--yes` to record them as offloaded and delete the local `.m4b` files. Future syncs skip offloaded titles. The command does not verify Audiobookshelf uploads; it intentionally trusts your confirmation and refuses to change anything if a local `.m4b` cannot map unambiguously to one Audible title.

The rest break the pipeline into steps: `download` exports your library and pulls anything missing (retrying books whose media disappeared), `convert` turns `.aax`/`.aaxc` into `.m4b`, `clean` deletes `.aax`, `.aaxc`, `.voucher`, and `-chapters.json` files once their `.m4b` exists — it leaves covers, PDFs, and anything else alone. `ready` lists what's listenable and what's still pending. `status` gives you counts (library size, tracked, offloaded, remaining, ready, pending); `status --table` lists every title with its state: `ready`, `needs_convert_aax`, `needs_convert_aaxc`, `offloaded`, `tracked_no_media`, or `not_downloaded`.

Downloads run one at a time on purpose. Audible itself is the bottleneck, and a single ordered flow keeps the tracking file from getting corrupted by concurrent writes.

A Taskfile wraps all of this if you'd rather type less: `task sync`, `task plan`, `task download`, `task convert`, `task clean`, `task ready`, `task status`, plus `task docker-sync` and `task docker-build` for the Docker versions. `task all` and `task docker-all` are deprecated aliases for `sync`/`docker-sync`.

## How it works

`auto-audible` checks that `audible-cli` is installed and configured, then exports your library as JSON (JSON replaced TSV a while back — less brittle when Audible tweaks its export format). Completed downloads are tracked in `<media-dir>/.auto-audible.json`; if you're using the default `media/` directory and have an old `downloaded_asins.json` lying around, it gets imported once automatically.

Books that belong to a series go into a subfolder named after the series and get renamed with a `## - ` prefix so they sort correctly:

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

Standalone books stay at the top level. `convert`, `clean`, `offload`, `ready`, and `status` all walk the full `media/` tree recursively, so the folder structure doesn't matter to them.

For conversion, `auto-audible` asks `audible-cli` for activation bytes and hands off to `ffmpeg`. `.aaxc` files need their matching `.voucher` file (same basename) alongside them — that's where the decryption key and IV live. No voucher, no conversion.

## Docker

The image bundles `auto-audible`, `audible-cli`, and `ffmpeg`.

```bash
docker build -t auto-audible .
```

```bash
docker run --rm -it \
  -v "$HOME/.audible:/root/.audible" \
  -v "$PWD:/work" \
  -w /work \
  auto-audible sync
```

Same flags as the Go binary work here — profile, password, whatever:

```bash
docker run --rm -it \
  -v "$HOME/.audible:/root/.audible" \
  -v "$PWD:/work" \
  -w /work \
  auto-audible download --profile audible
```

If your Audible config lives somewhere other than `~/.audible`, mount that path instead and set `AUDIBLE_CONFIG_DIR` to match.

## Development

```bash
go test -race -cover ./...
go vet ./...
go build ./...
```

CI runs the race-enabled tests and `go vet` on every push and PR. Dependabot keeps Go modules and Actions up to date weekly, but it doesn't touch Devbox — update `devbox.lock` manually with `devbox update`.

## License

MIT. See [LICENSE](LICENSE).

### Changes

| Pass | What changed | Examples |
|-|-|-|
| Structure | Removed "What changed" changelog section; folded load-bearing facts into Requirements/How it works | password-optional, JSON export -> now stated as current fact, not history |
| Repetition | Merged Commands, Auth behavior, and Taskfile sections; commands now listed once | 3 command lists -> 1 |
| Inflation | Cut promotional framing | "The repository now includes" -> "The image bundles" |
| Grammar | Fixed copula/-ing padding in command descriptions | "detects whether... and offers to" -> "will notice... and offer to" |
| Rhythm | Broke up uniform per-command bullet list into prose with varied sentence length | Commands section rewritten as paragraphs |
| Filler | Cut "Verification" section (redundant with Development/CI) | removed entirely |
