# Status Command

Add `--status` to show backup health per snapshot: last backup time, snapshot count, source path health, and optionally repo size.

## Motivation

After running backups there's no way to see what state things are in without manually running restic commands per repo. A status view gives a quick health check across snapshots on a location, catches mount failures, and shows what's initialized vs not.

## CLI Interface

```
--status <name|comma-list|all>
```

Accepts a single snapshot name, comma-separated names, or `all`. Uses `--location` if specified, otherwise `defaults.defaultLocation`.

Combined with existing flags:
- `--status all` — all snapshots, default location
- `--status all --location dogutil` — all snapshots on dogutil
- `--status d-immich,d-photos` — specific snapshots
- `-v` / not `-q` triggers verbose mode (adds size column)

## Default Output

```
SNAPSHOT      LOCATION  LAST BACKUP           SNAPSHOTS  SOURCE
d-immich      drobo     2026-04-25 03:00:05   5          ok
d-paperless   drobo     not initialized       -          ok
d-movies      drobo     2026-04-24 03:00:02   3          NOT MOUNTED
d-photos      drobo     2026-04-25 03:15:22   8          empty
```

Runs `restic snapshots --json` per repo (one restic call per snapshot).

## Verbose Output (`-v`)

```
SNAPSHOT      LOCATION  LAST BACKUP           SNAPSHOTS  SIZE        SOURCE
d-immich      drobo     2026-04-25 03:00:05   5          516.9 GiB   ok
d-paperless   drobo     not initialized       -          -           ok
d-movies      drobo     2026-04-24 03:00:02   3          85.2 GiB    NOT MOUNTED
d-photos      drobo     2026-04-25 03:15:22   8          142.1 GiB   empty
```

Adds `restic stats --json` per repo (slower — two restic calls per snapshot).

## Columns

| Column | Source | Always shown |
|--------|--------|-------------|
| SNAPSHOT | config snapshot name | yes |
| LOCATION | resolved location name | yes |
| LAST BACKUP | latest snapshot `time` from `restic snapshots --json` | yes |
| SNAPSHOTS | count of snapshots from `restic snapshots --json` | yes |
| SIZE | `total_size` from `restic stats --json` | verbose only |
| SOURCE | path accessibility check | yes |

## Source Path Check

Before running restic commands, check each path in `snap.Path`:

| State | Meaning |
|-------|---------|
| `ok` | All paths exist and are non-empty |
| `empty` | At least one path exists but has no contents |
| `NOT MOUNTED` | At least one path doesn't exist or isn't accessible |

Uses the same `os.ReadDir` / `os.Stat` logic as the existing `isDirEmpty` check in `restic.go`.

## Repo State Handling

| Repo state | LAST BACKUP | SNAPSHOTS | SIZE |
|------------|-------------|-----------|------|
| Not initialized (restic snapshots fails) | `not initialized` | `-` | `-` |
| Initialized, no snapshots yet | `never` | `0` | `0 B` |
| Initialized with snapshots | formatted time | count | formatted size |
| Unreachable (mount down, network) | `unreachable` | `-` | `-` |

Distinguish "not initialized" from "unreachable" by checking if `repoPath` base directory exists first (`os.Stat` on `loc.RepoBase`). If the base isn't accessible, it's unreachable. If the base is accessible but restic fails, it's not initialized.

## Restic JSON Formats

`restic snapshots --json` returns:
```json
[
  {
    "time": "2026-04-25T03:00:05.123456789Z",
    "hostname": "vrestic-29609460-rnkbg",
    "paths": ["/mnt/oatmeal/immich/library"],
    "short_id": "ffddc6fe"
  }
]
```

`restic stats --json` returns:
```json
{
  "total_size": 554984587264,
  "total_file_count": 42301
}
```

## Implementation

### New in `pkg/restic/restic.go`

`Runner.Status(snapshotName, snap)` method that:
1. Checks source paths (returns source health)
2. Resolves repo path from location
3. Runs `restic snapshots --json` via `shell.RunCapture` (new helper — same as `shell.Run` but captures stdout instead of passing to terminal)
4. Parses JSON, extracts snapshot count and latest time
5. If verbose, runs `restic stats --json`, parses total_size
6. Returns a `StatusResult` struct

```go
type StatusResult struct {
    Snapshot     string
    Location     string
    LastBackup   time.Time // zero value = never
    SnapshotCount int
    TotalSize    int64     // bytes, -1 = unknown
    Source       string    // "ok", "empty", "NOT MOUNTED"
    RepoState    string   // "ok", "not initialized", "unreachable"
}
```

### New in `pkg/shell/shell.go`

`RunCapture(Command) ([]byte, error)` — same as `Run` but captures stdout into a byte slice instead of writing to os.Stdout. Stderr still goes to os.Stderr.

### Changes in `cmd/backup/root.go`

- New `opts.statusSnapshot` string flag
- `--status` block in RunE: resolve names (single/comma/all), call `runner.Status()` per snapshot, format and print table
- `formatSize(bytes int64) string` helper for human-readable sizes (B, KiB, MiB, GiB, TiB)
- Time formatting: `2006-01-02 15:04:05` (local time)

### No changes to

- Config structs (no new config fields)
- Vault (status uses existing ReadPassword)
- Metrics (status doesn't push metrics)
