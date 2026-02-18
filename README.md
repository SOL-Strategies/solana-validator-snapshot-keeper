# solana-validator-snapshot-keeper

A (majority vibe coded) Solana validator-aware snapshot finder. Based on and inspired by [c29r3/solana-snapshot-finder](https://github.com/c29r3/solana-snapshot-finder) (no longer available).

## How It Works

When run on a node with a validator service running, the program will:

1. **Check identity** — if the validator is active (voting), skip entirely (never risk impacting vote latency)
2. **Assess freshness** — if local snapshots are recent enough, skip the cycle
3. **Snapshot nodes discovery** — probe the cluster for nodes serving snapshots
4. **Download** — parallel segmented HTTP download from the fastest node
5. **Prune** — remove old snapshots, keep only the most recent once done to avoid disk bloat

### Active vs Passive

- **Passive validator** — downloads snapshots normally
- **Active validator** — skips entirely (downloading could impact voting, turbine, replay)
- **Validator RPC unreachable** — proceeds with download (validator likely down)
- **Becomes active mid-download (failover)** — aborts immediately, cleans up temp files

## Installation

### Build from source

```bash
# Requires Go 1.24+
make build

# Binary at bin/solana-validator-snapshot-keeper
```

### Install

```bash
# Copy binary
sudo cp bin/solana-validator-snapshot-keeper /usr/local/bin/

# Create config directory
sudo mkdir -p /etc/solana-validator-snapshot-keeper

# Copy and edit config
sudo cp config.yml /etc/solana-validator-snapshot-keeper/config.yml
sudo vi /etc/solana-validator-snapshot-keeper/config.yml

# Install systemd service
sudo cp solana-validator-snapshot-keeper.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now solana-validator-snapshot-keeper
```

## Configuration

```yaml
log:
  level: info                            # debug, info, warn, error
  format: text                           # text, json, logfmt
  disable_timestamps: false              # set true to hide timestamps; overridden by --log-disable-timestamps

validator:
  rpc_url: "http://127.0.0.1:8899"
  active_identity_pubkey: ""             # (required) pubkey of the active validator identity

cluster:
  name: "mainnet-beta"                   # "mainnet-beta" or "testnet"
  # rpc_url: ""                          # override (auto-derived from cluster name)

snapshots:
  directory: "/mnt/accounts/snapshots"
  discovery:
    candidates:
      min_suitable_full: 3               # stop probing once N suitable full snapshot nodes found
      min_suitable_incremental: 5        # stop probing once N suitable incremental snapshot nodes found
      sort_order: latency                # "latency" or "slot_age"
    probe:
      concurrency: 500                   # concurrent HEAD probes
      max_latency: 100ms                 # max HEAD probe latency (duration string)
  download:
    min_speed: 60mb                      # minimum speed to accept a node (e.g. 60mb, 500kb, 1gb)
    min_speed_check_delay: 7s            # delay before checking min_speed (duration string)
    timeout: 30m                         # hard timeout per download (duration string)
    connections: 8                       # parallel HTTP Range connections (if server supports it)
  age:
    remote:
      max_slots: 1300                    # max slot age for candidate nodes on the network
    local:
      max_incremental_slots: 1300        # skip if local tip within this many slots; else incremental or full

hooks:
  on_success:
    - name: notify-slack
      cmd: /usr/local/bin/slack-notify.sh
      args: ["success", "Downloaded snapshot slot {{ .SnapshotSlot }} from {{ .SourceNode }}"]
      allow_failure: true
  on_failure:
    - name: notify-slack
      cmd: /usr/local/bin/slack-notify.sh
      args: ["failure", "Snapshot download failed: {{ .Error }}"]
      allow_failure: true
```

## Releasing

Releases are built and published automatically when you push a version tag on `master`:

```bash
git tag v1.0.0
git push origin v1.0.0
```

The [Release workflow](.github/workflows/release.yml) builds binaries for linux/amd64, linux/arm64, darwin/amd64, and darwin/arm64 (gzipped with sha256 checksums) and creates a GitHub Release with generated notes.

## Usage

### Run once (e.g. from a startup script)

```bash
solana-validator-snapshot-keeper run --config /etc/solana-validator-snapshot-keeper/config.yml
```

### Run on an interval (systemd service)

```bash
solana-validator-snapshot-keeper run \
    --config /etc/solana-validator-snapshot-keeper/config.yml \
    --on-interval 4h
```

## Hooks

Hooks run external commands on success or failure. Commands support Go template variables:

| Variable                 | Description                            |
| ------------------------ | -------------------------------------- |
| `{{ .SnapshotSlot }}`    | Slot number of the downloaded snapshot |
| `{{ .SnapshotType }}`    | `"full"` or `"incremental"`            |
| `{{ .SourceNode }}`      | RPC address of the source node         |
| `{{ .DownloadTimeSec }}` | Download duration in seconds           |
| `{{ .DownloadSizeMB }}`  | Download size in megabytes             |
| `{{ .SnapshotPath }}`    | Full path to the downloaded file       |
| `{{ .ClusterName }}`     | Cluster name from config               |
| `{{ .ValidatorRole }}`   | `"passive"` or `"unknown"`             |
| `{{ .Error }}`           | Error message (on_failure hooks only)  |

Each hook supports:
- `allow_failure: true` — log failure but continue to next hook
- `disabled: true` — skip without removing from config
- `stream_output: true` — stream stdout/stderr through the logger
- `environment:` — template-interpolated environment variables

## Lock File

A lock file at `<snapshot_path>/solana-validator-snapshot-keeper.lock` prevents concurrent instances. The file contains the PID and start time. Stale locks from dead processes are automatically overwritten.

No lock file present = no instance running.

## Development

### Local testing with mock server

```bash
# Terminal 1: start mock Solana RPC + snapshot server
make dev-mock-server

# Terminal 2: run keeper against mock
make dev
```

### Run tests

```bash
make test          # with verbose output + race detector
make test-cover    # with coverage report
```

### Build

```bash
make build         # main binary
make build-mock    # mock server
make build-all     # both
```

## Architecture

```
cmd/                    CLI (Cobra + koanf)
internal/config/        Config loading + validation
internal/constants/     Cluster names, RPC URLs
internal/rpc/           Solana JSON-RPC client (net/http)
internal/discovery/     Node probing + ranking (concurrent HEAD requests)
internal/downloader/    Parallel segmented HTTP download (File.WriteAt)
internal/pruner/        Snapshot file management
internal/hooks/         Templated command execution (os/exec)
internal/keeper/        Orchestrator (freshness -> identity -> download -> prune)
internal/manager/       Run loop + file lock
mock-server/            Standalone mock for local development
```

### Main differences to solana-snapshot-finder

| Area               | solana-snapshot-finder (Python) | solana-validator-snapshot-keeper (Go)      |
| ------------------ | ------------------------------- | ------------------------------------------ |
| Download           | Single-connection wget          | Parallel segmented HTTP (8 connections)    |
| Speed test         | Downloads data then discards it | Inline — speed test IS the download        |
| Resume             | None — restarts from zero       | HTTP Range resume on retry                 |
| Freshness          | Always runs full discovery      | Skips if local snapshots are fresh         |
| Tiered             | Always downloads full           | Incremental-only when full is still usable |
| Dependencies       | wget, Python, jq                | Single Go binary                           |
| Identity-aware     | No                              | Skips on active, aborts on failover        |
| Notifications      | None                            | Configurable hook commands                 |
| Concurrency safety | Python GIL + race conditions    | Go goroutines + channels                   |
| Artifacts          | Drops .log in snapshot dir      | Only snapshots + lock file                 |
