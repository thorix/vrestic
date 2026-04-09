# vrestic

Scheduled restic backup manager with Vault integration for password management.

## Features

- Reads backup configuration from a YAML config file
- Auto-generates and stores restic passwords in HashiCorp Vault (`kv/rbackup/<name>`)
- Supports multiple backup snapshots with independent retention policies
- Dual-repo support: `repo` (primary/remote) and `localRepo` (local/in-house backup)
- Auto-skips `limitUpload` for local filesystem repos (only applies to remote backends like sftp, s3)

## Usage

```bash
# Back up a specific snapshot
vrestic --backup d-immich --in /config/config.yaml

# Back up all snapshots
vrestic --all --in /config/config.yaml

# Use local repo paths instead of primary
vrestic --local --backup d-immich --in /config/config.yaml

# Dry run
vrestic --dry-run --backup d-immich --in /config/config.yaml

# List snapshots
vrestic --list d-immich --in /config/config.yaml
```

## Config Format

```yaml
snapshots:
  d-immich:
    path:
      - /mnt/oatmeal/immich/library
      - /mnt/oatmeal/immich/backups
    repo: /mnt/backup/FAC35D351AFBAE75        # Primary backup destination
    localRepo: /media/thorix/backup8/restic/... # Local USB drive (--local flag)
    retention: "6m"
    limitUpload: 2048  # KiB/s - only applied to remote repos (sftp:, s3:, etc.)
```

### Local vs Remote Repos

- **Local repos** (`/path/to/repo`): Filesystem paths. `limitUpload` is automatically skipped since bandwidth limiting is unnecessary for local I/O.
- **Remote repos** (`sftp:user@host:/path`, `s3:url`, etc.): Remote backends where `limitUpload` is respected to avoid saturating upload bandwidth.

The `--local` flag selects `localRepo` instead of `repo` for each snapshot.

## Kubernetes Deployment

Deployed as a CronJob via the `simple-service` Helm chart. Config is stored in Vault at `kv/vrestic/config` and synced as a Kubernetes secret.

Mount paths in the container match the config file paths:
- `/mnt/oatmeal/immich` — source data (SMB from NAS, read-only)
- `/mnt/backup` — backup destination (SMB from Drobo NAS, read-write)

## Vault Integration

vrestic uses Kubernetes auth to access Vault:
- **Read** `kv/vrestic/config` — backup configuration
- **Read** `kv/vrestic/ssh` — SSH key for SFTP backends
- **Read/Write** `kv/rbackup/*` — auto-managed restic passwords per snapshot
