# Named Backup Locations

Replace the binary `--local` / remote model with named locations, allowing a single snapshot to back up to multiple destinations (e.g., local drobo NAS and remote dogutil) selected at runtime.

## Motivation

The current config has `repoBase` (cron/remote) and `localRepoBase` (local), toggled by a `--local` flag. This is limited to exactly two destinations and the naming is misleading. In practice, the local drobo is the reliable always-on target and remote locations like dogutil are best-effort for a subset of snapshots. Named locations make this explicit, extensible, and allow per-location settings like `limitUpload`.

## Config Schema

```yaml
defaults:
  defaultLocation: drobo
  retention: 6m
  metricsURL: http://victoria-metrics-single-server.victoriametrics.svc:8428/api/v1/import/prometheus
  timeout: 4h
  locations:
    drobo:
      repoBase: /mnt/backup/
    dogutil:
      repoBase: /mnt/dogutil/
      limitUpload: 2048
      cacheDir: /tmp/restic-cache

snapshots:
  d-immich:
    repoName: FAC35D351AFBAE75
    path:
      - /mnt/oatmeal/immich/library
      - /mnt/oatmeal/immich/backups
  d-paperless:
    repoName: 7AEF975276E4BB5B
    path:
      - /mnt/oatmeal/paperless
    retention: 3m
```

### Defaults (top-level)

Global settings that are not location-specific:

| Field | Type | Description |
|-------|------|-------------|
| `defaultLocation` | string | Location used when `--location` is omitted |
| `retention` | string | Default retention period (restic duration format) |
| `metricsURL` | string | Prometheus push endpoint |
| `timeout` | string | Per-snapshot deadline (Go duration) |

### Location

Destination-specific settings under `defaults.locations.<name>`:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repoBase` | string | yes | Base path for repos (e.g., `/mnt/backup/`, `sftp:host:/path/`) |
| `limitUpload` | int | no | Max upload speed in KiB/s |
| `cacheDir` | string | no | Restic cache directory |
| `retention` | string | no | Override global retention for this location |

### Snapshot

Per-backup-target definition under `snapshots.<name>`:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repoName` | string | yes | 16-char hex ID, joined with location's `repoBase` |
| `path` | string or list | yes | Source directories to back up |
| `exclude` | string | no | Exclusion pattern |
| `retention` | string | no | Override retention for this snapshot |
| `limitUpload` | int | no | Override upload limit for this snapshot |
| `cacheDir` | string | no | Override cache dir for this snapshot |

### Removed Fields

These fields are removed with no replacement:

- `defaults.repoBase` — replaced by `defaults.locations.<name>.repoBase`
- `defaults.localRepoBase` — replaced by `defaults.locations.<name>.repoBase`
- `snapshot.repo` — replaced by `repoName` + location resolution
- `snapshot.localRepo` — replaced by `repoName` + location resolution

### Setting Resolution Order

For `limitUpload`, `cacheDir`, and `retention`:

1. Per-snapshot value (if non-zero)
2. Location-level value (if non-zero)
3. Top-level default (for `retention` only; `limitUpload` and `cacheDir` have no top-level default)

`limitUpload` is still skipped for local filesystem paths regardless of config (existing `isRemoteRepo` check stays).

## CLI Changes

### New: `--location <name>`

Selects which location to back up to. Defaults to `defaults.defaultLocation` when omitted. Validated against `defaults.locations` keys — errors if the name doesn't exist.

Replaces the `--local` flag, which is removed.

### Changed: `--backup` accepts comma-separated names

- `--backup d-immich` — single snapshot
- `--backup d-movies,d-photos,d-paperless` — multiple, run sequentially
- Splits on comma, trims whitespace
- Validates all names exist in config before running any (fail-fast)

### New: `--init <name>`

Initializes a restic repo for an existing snapshot on a specific location. Separated from `--new` because `--new` creates a brand new snapshot (and errors if the Vault password already exists), while `--init` works with existing snapshots.

Behavior:
- Requires the snapshot to already exist in config
- Reads the existing password from Vault (does not create one)
- Runs `restic init` at `location.repoBase + repoName`
- Validates the target path doesn't already contain a repo
- Accepts `--location` to target a specific location (defaults to `defaultLocation`)
- No config changes needed

Workflow for adding a new location to an existing snapshot:
```bash
# 1. Add location to config defaults.locations
# 2. Init the repo
vrestic --init d-immich --location newloc
# 3. Back up
vrestic --backup d-immich --location newloc
```

### Changed: `--new`

Still creates a new snapshot interactively. Changes:
- No longer prompts for "Repo base directory" or "Local repo base directory" — uses the selected location's `repoBase`
- Accepts `--location` to control where the repo is initialized (defaults to `defaultLocation`)
- Still generates `repoName`, prompts for source paths, retention
- Still creates password in Vault, uploads config

### Removed: `--local`

Replaced by `--location`. No deprecation period — clean removal.

### Unchanged

`--all`, `--unlock`, `--list`, `--sync-config`, `--generate`, `--dry-run`, `--quiet`, `--debug` are unchanged in behavior. `--all` and `--unlock` respect the selected `--location`.

## Go Struct Changes

### `pkg/config/config.go`

```go
type Defaults struct {
    DefaultLocation string               `yaml:"defaultLocation,omitempty"`
    Retention       string               `yaml:"retention,omitempty"`
    MetricsURL      string               `yaml:"metricsURL,omitempty"`
    Timeout         string               `yaml:"timeout,omitempty"`
    Locations       map[string]*Location  `yaml:"locations,omitempty"`
}

type Location struct {
    RepoBase    string `yaml:"repoBase"`
    LimitUpload int    `yaml:"limitUpload,omitempty"`
    CacheDir    string `yaml:"cacheDir,omitempty"`
    Retention   string `yaml:"retention,omitempty"`
}

type Snapshot struct {
    RepoName    string     `yaml:"repoName,omitempty"`
    Path        StringList `yaml:"path,omitempty"`
    Exclude     string     `yaml:"exclude,omitempty"`
    Retention   string     `yaml:"retention,omitempty"`
    LimitUpload int        `yaml:"limitUpload,omitempty"`
    CacheDir    string     `yaml:"cacheDir,omitempty"`
}
```

`ResolvedRepo` changes signature:

```go
func (s *Snapshot) ResolvedRepo(loc *Location) string {
    if s.RepoName != "" && loc.RepoBase != "" {
        return filepath.Join(loc.RepoBase, s.RepoName)
    }
    return ""
}
```

New helpers for three-tier resolution:

```go
func (s *Snapshot) ResolvedLimitUpload(loc *Location) int
func (s *Snapshot) ResolvedCacheDir(loc *Location) string
func (s *Snapshot) ResolvedRetention(loc *Location, defaults Defaults) string
```

### `pkg/restic/restic.go`

`Runner` struct changes:

```go
type Runner struct {
    DryRun       bool
    Verbose      bool
    Location     *config.Location  // replaces UseLocal bool
    LocationName string
    Vault        vault.Vault
    Defaults     config.Defaults
    Metrics      *metrics.Pusher
    Timeout      time.Duration
    ctx          context.Context
}
```

All call sites that used `snap.ResolvedRepo(r.Defaults, r.UseLocal)` change to `snap.ResolvedRepo(r.Location)`.

All call sites that read `snap.LimitUpload` or `r.Defaults.LimitUpload` change to `snap.ResolvedLimitUpload(r.Location)`, etc.

### `cmd/backup/root.go`

- `opts.useLocal` removed, `opts.location` added (string)
- `opts.runSnapshot` accepts comma-separated values
- New `opts.initSnapshot` for `--init`
- Location resolution: look up `opts.location` (or `cfg.Defaults.DefaultLocation`) in `cfg.Defaults.Locations`, error if not found
- Comma-split and validate snapshot names before running any

## Metrics

The `location` label is added to all pushed metrics:

```
vrestic_backup_success{snapshot="d-immich",location="drobo"} 1
vrestic_backup_duration_seconds{snapshot="d-immich",location="drobo"} 3600
vrestic_backup_last_run_timestamp{snapshot="d-immich",location="drobo"} 1745000000
vrestic_backup_last_success_timestamp{snapshot="d-immich",location="drobo"} 1745000000
```

`metrics.Result` gains a `Location string` field. `metrics.Pusher.Push` includes it in the label set.

## Vault / Passwords

No changes. Passwords are keyed by snapshot name and shared across all locations — the same restic repo password works regardless of which `repoBase` is used. The Vault layout (`kv/vrestic/passwords`, `kv/vrestic/config`) is unchanged.

## Migration

Existing config files need a one-time manual update:

```yaml
# Before
defaults:
  repoBase: /mnt/backup/
  localRepoBase: /mnt/dogutil/
  limitUpload: 2048

# After
defaults:
  defaultLocation: drobo
  locations:
    drobo:
      repoBase: /mnt/backup/
    dogutil:
      repoBase: /mnt/dogutil/
      limitUpload: 2048
```

Per-snapshot `repo` and `localRepo` fields (if any) need to be converted to use `repoName` with an appropriate location. The `config.yaml.example` file will be updated to reflect the new schema.

Kubernetes CronJob commands change:
- `--local` flag removed from any CronJob args
- `--location <name>` added where non-default location is needed

After updating config in Vault (`--sync-config` to pull, edit, re-upload or `--new` to push), the CronJobs will pick up the new format.

## Example Usage

```bash
# Back up immich to drobo (default location)
vrestic --backup d-immich

# Back up select snapshots to dogutil
vrestic --backup d-movies,d-photos --location dogutil

# Back up everything to drobo
vrestic --all

# Init an existing snapshot on a new location
vrestic --init d-immich --location dogutil

# Create a brand new snapshot (inits on drobo by default)
vrestic --new d-newbackup

# Create a brand new snapshot, init on dogutil
vrestic --new d-newbackup --location dogutil

# List snapshots (shows default location paths)
vrestic --list
```
