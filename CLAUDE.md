# Claude Code Project Configuration

## Project Context

vrestic is a Go CLI tool that manages restic backups with HashiCorp Vault for password storage. It runs in two modes:
- **CronJob (Kubernetes)**: Read-only Vault access, runs scheduled backups
- **Local CLI**: Read-write Vault access for creating new repos (`--new`) and syncing config

## Architecture

```
cmd/backup/root.go    — CLI entry point, cobra flags, interactive prompts
pkg/config/config.go  — YAML config parsing, Defaults + Snapshot structs
pkg/restic/restic.go  — Restic command orchestration (backup, unlock, forget)
pkg/vault/vault.go    — Vault client (passwords, config read/write)
pkg/shell/shell.go    — Shell command execution wrapper
```

## Config Design

The config uses a `defaults` block to avoid repetition:

```yaml
defaults:
  repoBase: /mnt/backup/        # Remote/cron repo base
  localRepoBase: /mnt/backup/   # Local repo base (--local flag)
  retention: "6m"
  limitUpload: 2048

snapshots:
  d-immich:
    repoName: FAC35D351AFBAE75  # Joined with repoBase/localRepoBase
    path:
      - /mnt/oatmeal/immich/library
```

Path resolution: `snap.ResolvedRepo(defaults, useLocal)` builds full path from `defaults.repoBase` + `repoName`. Explicit `repo`/`localRepo` fields override when needed.

## Vault Layout

```
kv/vrestic/passwords   — Single secret, snapshot names as keys (flat map)
kv/vrestic/config      — Full config.yaml under "config.yaml" key
kv/vrestic/ssh         — SSH key for SFTP backends
```

Passwords are consolidated in one secret (not per-snapshot secrets). WritePassword does read-merge-write to avoid clobbering other entries.

## Key Design Decisions

- **Vault is authoritative**: `--new` refuses to overwrite existing Vault passwords
- **CronJob is read-only**: No auto-create of passwords or repos in cron mode
- **Local `--new` handles all creation**: Interactive prompts, restic init, Vault writes, config upload
- **File-based fallback**: `RESTIC_SECRETS_DIR` env var skips Vault entirely (reads passwords from mounted files)
- **Repo base validation**: Warns (doesn't error) when remote path isn't accessible locally

## Building and Testing

```bash
# Build
go build -o vrestic ./

# Run locally (needs VAULT_ADDR + VAULT_TOKEN)
export VAULT_ADDR=https://vault.thorix.io
export VAULT_TOKEN=$(vault print token)
go run ./ --list
go run ./ --new d-something
go run ./ --local --backup d-something

# Dry run (no restic execution)
go run ./ --dry-run --backup d-immich
```

## Docker Image

Built and pushed to `harbor.thorix.io/library/vrestic:<tag>`. The Kubernetes CronJob uses this image. Update the tag in `kubernetes-deploy/vrestic/values.yaml` after building a new version.

## Common Workflows

### Adding a new backup
```bash
go run ./ --new d-mybackup --in config.yaml
# Follow prompts, then:
go run ./ --local --backup d-mybackup --in config.yaml
```

### Syncing config from Vault
```bash
go run ./ --sync-config --in config.yaml
```

### After code changes
1. `go build ./...` to verify compilation
2. Test locally with `--dry-run` or `--local`
3. Build Docker image, push to harbor
4. Update image tag in kubernetes-deploy/vrestic/values.yaml
5. ArgoCD syncs the CronJob

## Metrics and Observability

`pkg/metrics/metrics.go` pushes to a Prometheus pushgateway-compatible endpoint (VictoriaMetrics) after each backup. No external dependencies — uses raw HTTP POST with Prometheus text format.

Metrics pushed per snapshot:
- `vrestic_backup_success` — 1/0 gauge
- `vrestic_backup_duration_seconds` — wall-clock time
- `vrestic_backup_last_run_timestamp` — when it ran
- `vrestic_backup_last_success_timestamp` — last success (only set on success)

Push is best-effort (5s timeout, logged warning on failure, never blocks).

Config:
```yaml
defaults:
  metricsURL: http://victoria-metrics-single-server.victoriametrics.svc:8428/api/v1/import/prometheus
  timeout: "4h"
```

Alert rules live in vmalert. Key alerts:
- `VresticBackupStale`: no success in 48h
- `VresticBackupFailed`: last run returned failure

## Timeout

`defaults.timeout` sets a per-snapshot deadline. Shell commands (`restic backup`, `restic forget`) are killed via `context.WithTimeout` if exceeded. Protects the CronJob from hung NFS mounts or stuck processes.

## Conventions

- No auto-initialization of repos in the backup path — error with "use --new"
- Retention and limitUpload default from config, override per-snapshot
- `limitUpload` is skipped automatically for local filesystem repos
- After backup, stale locks are cleared and retention policy is applied (forget + prune)
- `RunTimed()` is the entry point for backups — wraps `Run()` with timing, metrics, and timeout
- Metrics are disabled when `metricsURL` is empty (local dev)
