# vrestic

Scheduled restic backup manager with Vault integration for password management.

## Features

- YAML-driven backup configuration with sensible defaults
- Passwords stored in HashiCorp Vault (`kv/vrestic/passwords`)
- Interactive `--new` command for creating new backup repos
- Dual-mode: CronJob in Kubernetes (read-only) and local CLI (read-write for setup)
- Supports multiple backup snapshots with independent retention policies
- Auto-skips `limitUpload` for local filesystem repos

## Usage

```bash
# Create a new backup snapshot (interactive)
vrestic --new d-paperless --in config.yaml

# Back up a specific snapshot
vrestic --backup d-immich --in config.yaml

# Back up all snapshots
vrestic --all --in config.yaml

# Use local repo paths instead of primary
vrestic --local --backup d-immich --in config.yaml

# List snapshots
vrestic --list --in config.yaml

# Unlock stale locks
vrestic --unlock --backup d-immich
vrestic --unlock --all

# Pull config from Vault to local file
vrestic --sync-config --in config.yaml

# Generate a random repo name
vrestic --generate

# Dry run (print commands without executing)
vrestic --dry-run --backup d-immich
```

## Config Format

```yaml
defaults:
  repoBase: /mnt/backup/          # Base path for restic repos (cron/remote)
  localRepoBase: /mnt/backup/     # Base path for --local backups
  retention: "6m"                  # Default retention policy (keep-within)
  limitUpload: 2048               # Default upload limit KiB/s (remote only)

snapshots:
  d-immich:
    repoName: FAC35D351AFBAE75    # Repo directory name (joined with repoBase)
    path:
      - /mnt/oatmeal/immich/library
      - /mnt/oatmeal/immich/backups

  d-paperless:
    repoName: E7BAC7F5C37CB49D
    path:
      - /mnt/oatmeal/paperless
    localRepo: /mnt/dogutil/E7BAC7F5C37CB49D  # Override when local differs
```

### Path Resolution

The full repo path is built from `defaults.repoBase` + `repoName` (or `defaults.localRepoBase` + `repoName` with `--local`). Explicit `repo` and `localRepo` fields override this when a snapshot needs a non-standard path.

### Per-Snapshot Overrides

Any field in a snapshot overrides the default. If `retention` or `limitUpload` is omitted, the value from `defaults` is used. If set on the snapshot, it takes precedence.

### Local vs Remote Repos

- **Local repos** (`/path/to/repo`): Filesystem paths. `limitUpload` is automatically skipped.
- **Remote repos** (`sftp:user@host:/path`, `s3:url`, etc.): `limitUpload` is applied to avoid saturating upload bandwidth.

## Creating New Backups

`vrestic --new <name>` handles the full setup interactively:

1. Checks Vault for an existing password (refuses to overwrite)
2. Prompts for source path(s) and validates they exist
3. Generates a random repo directory name
4. Prompts for repo base (uses defaults from config)
5. Initializes the restic repo at the local path
6. Writes the password to Vault (`kv/vrestic/passwords`)
7. Updates the local config file
8. Uploads the config to Vault (`kv/vrestic/config`)

After `--new`, run your first backup with `vrestic --backup <name>`.

## Kubernetes Deployment

Deployed as a CronJob via the `simple-service` Helm chart. The CronJob has **read-only** Vault access — all repo creation is done locally via `--new`.

Mount paths in the container match the config file paths:
- `/mnt/oatmeal/immich` — source data (SMB from NAS, read-only)
- `/mnt/backup` — backup destination (SMB from Drobo NAS, read-write)

Config is stored in Vault at `kv/vrestic/config` and synced as a Kubernetes secret via VSO.

## Vault Layout

```
kv/vrestic/passwords   — consolidated secret with snapshot names as keys
kv/vrestic/config      — full config.yaml stored under "config.yaml" key
kv/vrestic/ssh         — SSH key for SFTP backends
```

### Auth Modes

- **Kubernetes (CronJob)**: Service account token auth, read-only access to passwords
- **Local CLI**: `VAULT_TOKEN` env var, read-write access for `--new` and `--sync-config`
- **File-based (`RESTIC_SECRETS_DIR`)**: Skips Vault entirely, reads passwords from mounted files

## Metrics and Alerting

vrestic pushes metrics to a Prometheus pushgateway-compatible endpoint (VictoriaMetrics) after each backup completes. This enables alerting on backup failures without needing a sidecar or ServiceMonitor (CronJob pods are ephemeral).

### Metrics Pushed

| Metric | Description |
|--------|-------------|
| `vrestic_backup_success{snapshot="..."}` | 1 on success, 0 on failure |
| `vrestic_backup_duration_seconds{snapshot="..."}` | Wall-clock time of the backup |
| `vrestic_backup_last_run_timestamp{snapshot="..."}` | Unix timestamp of the last run |
| `vrestic_backup_last_success_timestamp{snapshot="..."}` | Unix timestamp of last success (only set on success) |

### Config

```yaml
defaults:
  metricsURL: http://victoria-metrics-single-server.victoriametrics.svc:8428/api/v1/import/prometheus
  timeout: "4h"
```

- `metricsURL` — pushgateway endpoint. Leave empty to disable metrics.
- `timeout` — maximum duration per snapshot. The backup is killed if it exceeds this (protects against hung NFS mounts).

Metrics push is best-effort: failures are logged as warnings but never block or fail a backup.

### Alert Rules (vmalert)

```yaml
groups:
- name: vrestic
  rules:
  - alert: VresticBackupStale
    expr: time() - vrestic_backup_last_success_timestamp > 48 * 3600
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Backup {{ $labels.snapshot }} hasn't succeeded in 48h"

  - alert: VresticBackupFailed
    expr: vrestic_backup_success == 0
    for: 0m
    labels:
      severity: critical
    annotations:
      summary: "Backup {{ $labels.snapshot }} failed"
      description: "Most recent backup run for {{ $labels.snapshot }} did not succeed."

  - alert: VresticBackupSlow
    expr: vrestic_backup_duration_seconds > 3 * 3600
    for: 0m
    labels:
      severity: warning
    annotations:
      summary: "Backup {{ $labels.snapshot }} took over 3 hours"
```

### Grafana Dashboard Queries

```promql
# Success rate over last 7 days
avg_over_time(vrestic_backup_success[7d]) * 100

# Duration trend per snapshot
vrestic_backup_duration_seconds

# Time since last successful backup (stale detection)
time() - vrestic_backup_last_success_timestamp
```

## Timeout

Each snapshot backup has a configurable timeout (default: no timeout). When set via `defaults.timeout`, restic commands are killed if they exceed the deadline. This prevents a single stuck backup from blocking the entire CronJob.

The timeout covers the full lifecycle: repo verification, backup, unlock, and retention pruning.

## Building

```bash
go build -o vrestic ./
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `VAULT_ADDR` | Vault server address |
| `VAULT_TOKEN` | Vault token (local use) |
| `VAULT_ROLE` | Kubernetes auth role (default: `vrestic`) |
| `RESTIC_SECRETS_DIR` | Directory with password files (skips Vault) |
| `RESTIC_CACHE_DIR` | Restic cache directory |
