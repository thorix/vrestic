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

The config uses named locations for backup destinations and a `defaults` block to avoid repetition:

```yaml
defaults:
  defaultLocation: drobo
  retention: "6m"
  metricsURL: http://victoriametrics-http.victoriametrics.svc:8428/api/v1/import/prometheus
  timeout: "4h"
  locations:
    drobo:
      repoBase: /mnt/drobo/backups/
    dogutil:
      repoBase: /mnt/dogutil/
      limitUpload: 2048
    pcloud:
      repoBase: rclone:pcloud:restic/
      limitUpload: 1220

snapshots:
  immich:
    repoName: FAC35D351AFBAE75
    path:
      - /mnt/oatmeal/immich/library
      - /mnt/oatmeal/immich/backups
```

Path resolution: `snap.ResolvedRepo(location)` joins `location.RepoBase` + `repoName`.

Setting resolution order: snapshot -> location -> global default.
- `limitUpload`, `cacheDir`: snapshot -> location (no global default)
- `retention`: snapshot -> location -> global default
- `limitUpload` is automatically skipped for local filesystem repos

**Important**: `config.yaml` is gitignored (contains paths to secrets). Source of truth is Vault at `kv/vrestic/config`. Push local edits with:
```bash
vault kv patch kv/vrestic/config "config.yaml=$(cat config.yaml)"
```
`--sync-config` pulls FROM Vault — it does not push.

## Vault Layout

```
kv/vrestic/passwords   — Single secret, snapshot names as keys (flat map)
kv/vrestic/config      — Full config.yaml under "config.yaml" key
kv/vrestic/ssh         — SSH private key (id_ed25519) + known_hosts for SFTP backends
kv/vrestic/rclone      — rclone.conf for pCloud OAuth token
kv/smb/oatmeal/*       — SMB credentials per NAS share
kv/smb/drobo/backups   — SMB credentials for Drobo destination
```

Passwords are consolidated in one secret (not per-snapshot secrets). WritePassword does read-merge-write to avoid clobbering other entries.

## Key Design Decisions

- **Vault is authoritative**: `--new` refuses to overwrite existing Vault passwords
- **CronJob is read-only**: No auto-create of passwords or repos in cron mode
- **Local `--new` handles creation**: Interactive prompts, restic init, Vault writes, config upload
- **`--init` for additional locations**: Inits an existing snapshot on a new location using the existing Vault password (use this, not `--new`, when the snapshot already exists in config)
- **Named locations**: `--location <name>` selects backup destination; defaults to `defaults.defaultLocation`
- **`--repo-base` override**: Cluster CronJobs pass `--repo-base` to override the location's repoBase — local config keeps human-friendly paths, cluster always uses the pod mount point
- **`--hostname` for stable parent lookup**: Without this, each pod gets a random hostname and restic can't find the previous snapshot as a parent, forcing a full rescan every run. Always pass `--hostname` in CronJob args.
- **`--timeout 0`**: Disables the timeout entirely — use for initial seeding runs on large repos
- **`--all`**: Backs up every snapshot in config; use `--backup a,b,c` to exclude specific ones
- **File-based fallback**: `RESTIC_SECRETS_DIR` env var skips Vault entirely (reads passwords from mounted files)

## CLI Flags Reference

```
--backup, -b    Snapshot name(s), comma-separated
--all           Run all snapshots in config
--location      Named location from config (default: defaultLocation)
--repo-base     Override location's repoBase at runtime
--hostname      Stable hostname for restic --host (critical for incremental backups in k8s)
--timeout       Override backup timeout; use "0" to disable entirely
--new           Create a new snapshot interactively (writes to Vault, inits repo)
--init          Initialize an existing snapshot on a new location (uses existing Vault password)
--dry-run       Print restic commands without running them
--list          List all configured snapshots
--status        Show recent snapshot health
--sync-config   Pull config.yaml FROM Vault to local file
--unlock        Remove stale restic locks
```

## Building and Testing

```bash
# Build
go build -o vrestic ./

# Run locally (needs VAULT_ADDR + VAULT_TOKEN)
export VAULT_ADDR=https://vault.thorix.io
export VAULT_TOKEN=$(vault print token)
go run ./ --list
go run ./ --new mysnapshot
go run ./ --backup immich

# Dry run (no restic execution)
go run ./ --dry-run --backup immich
go run ./ --dry-run --backup immich --location dogutil

# Initial seed of a large repo (no timeout, no rate limit)
go run ./ --backup immich --location pcloud --timeout 0 --hostname my-stable-name
```

## Docker Image

Built with podman and pushed to `harbor.thorix.io/library/vrestic:<tag>`.

```bash
podman build --build-arg CACHE_REGISTRY=docker.io/library/ -t harbor.thorix.io/library/vrestic:vX.Y.Z .
podman push harbor.thorix.io/library/vrestic:vX.Y.Z
```

`--build-arg CACHE_REGISTRY=docker.io/library/` is required — podman doesn't default short image names to docker.io.

Update the tag in `kubernetes-deploy/vrestic/values.prod.yaml` after pushing.

## Kubernetes CronJob Schedule

All jobs are in the `vrestic` namespace on the rye cluster.

| CronJob | Time | Snapshots | Notes |
|---|---|---|---|
| `vrestic-thorix` | 1 AM | thorix | 8h timeout — 1.7 TiB, 3M files, slow SMB stat |
| `vrestic` | 3 AM | audiobooks, ebooks, immich, movies, music, photos, randr, tvshows | drobo |
| `vrestic-dogutil` | 4 AM | immich | SFTP via Tailscale egress |
| `vrestic-pcloud` | 5 AM | immich | rclone to pCloud, ~10 Mbps cap |
| `vrestic-paperless` | 6 AM | paperless | runs after paperless DB backup at 4 AM |

## Kubernetes Operational Notes

### Vault role naming — CRITICAL
Every job-specific values file MUST explicitly set `vault.kubernetes.role` AND `VAULT_ROLE` env var. If omitted, the Helm chart defaults `spec.name` to the Release.Name (`vrestic`), which overwrites the main Vault auth role and locks out the primary CronJob with 403 errors.

```yaml
vault:
  kubernetes:
    role: vrestic-myjob   # REQUIRED — must match fullnameOverride
env:
- name: VAULT_ROLE
  value: "vrestic-myjob"  # REQUIRED — matches the above
```

### runAsUser per job type
- Drobo jobs: `runAsUser: 65534` (matches SMB mount uid, can write lock files)
- SFTP jobs (dogutil, pcloud): `runAsUser: 0` (SSH keys are root:root 0400)
- Base `values.yaml` uses `runAsUser: 65534`; SFTP jobs override with `securityContext: runAsUser: 0`

### Helm arrays replace — don't merge
Arrays (`env`, `volumeMounts`, `volumes`, `persistentVolumes`) in override values files REPLACE the base array entirely. Always include every entry needed by the job.

### Force VSO secret re-sync
```bash
kubectl annotate vaultstaticsecret <name> -n vrestic force-sync="$(date +%s)" --overwrite
```

### Diagnose a failing job
```bash
kubectl get jobs -n vrestic --sort-by=.metadata.creationTimestamp | tail -20
kubectl logs -n vrestic -l job-name=<job-name> | tail -20
```

### Common failure patterns
- `permission denied` on locks → uid mismatch (see runAsUser above)
- `403 Forbidden: service account name not authorized` → Vault role clobbered by another job missing explicit `vault.kubernetes.role`
- `command timed out` → increase `--timeout` or use `--timeout 0` for seeding; large SMB shares with millions of files hit this even with parent snapshots
- `no parent snapshot found` → `--hostname` not set or hostname changed; first run after hostname change does full rescan

## Common Workflows

### Adding a new backup destination for an existing snapshot
```bash
# 1. Initialize the repo (uses existing Vault password)
go run ./ --init mysnapshot --location pcloud --in config.yaml

# 2. Seed the first backup (disable timeout for large repos)
go run ./ --backup mysnapshot --location pcloud --timeout 0 --hostname stable-name --in config.yaml

# 3. Add CronJob values file in kubernetes-deploy/vrestic/
# 4. Add to deploy.yaml, commit, push
```

### Seeding a large repo locally before cluster takes over
```bash
# Use --hostname matching what the CronJob will use, so first cluster run is incremental
go run ./ --backup immich --location drobo --timeout 0 --hostname vrestic-drobo --in config.yaml
```

### Pushing config changes to Vault
```bash
vault kv patch kv/vrestic/config "config.yaml=$(cat config.yaml)"
# Then force VSO re-sync:
kubectl annotate vaultstaticsecret vrestic-config -n vrestic force-sync="$(date +%s)" --overwrite
```

### After code changes
1. `go build ./...` to verify compilation
2. Test locally with `--dry-run`
3. `podman build --build-arg CACHE_REGISTRY=docker.io/library/ -t harbor.thorix.io/library/vrestic:vX.Y.Z .`
4. `podman push harbor.thorix.io/library/vrestic:vX.Y.Z`
5. Update tag in `kubernetes-deploy/vrestic/values.prod.yaml`
6. Commit and push both repos

## Metrics and Observability

`pkg/metrics/metrics.go` pushes to VictoriaMetrics after each backup.

Correct metricsURL: `http://victoriametrics-http.victoriametrics.svc:8428/api/v1/import/prometheus`

Metrics pushed per snapshot (labels: `snapshot`, `location`, `job`):
- `vrestic_backup_success` — 1/0 gauge
- `vrestic_backup_duration_seconds` — wall-clock time
- `vrestic_backup_last_run_timestamp` — when it ran
- `vrestic_backup_last_success_timestamp` — last success (only set on success)

Push is best-effort (5s timeout, logged warning on failure, never blocks). "Failed to push metrics" warnings are non-fatal.

Alert rules live in vmalert:
- `VresticBackupStale`: no success in 48h
- `VresticBackupFailed`: last run returned failure

## Timeout

`defaults.timeout` sets a per-snapshot deadline applied to the entire `--all` or `--backup a,b,c` run. Use `--timeout` CLI flag to override per run. Use `--timeout 0` to disable entirely (initial seeding of large repos). Large SMB shares with millions of files can exceed 4h just on the stat scan — split them into separate jobs with longer timeouts.
