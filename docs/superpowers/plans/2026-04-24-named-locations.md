# Named Backup Locations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the binary `--local`/remote model with named locations so snapshots can target multiple backup destinations selected via `--location`.

**Architecture:** New `Location` struct in config, `--location` CLI flag replaces `--local`, three-tier setting resolution (snapshot -> location -> global). New `--init` command for initializing existing snapshots on additional locations. Comma-separated `--backup` flag for multi-snapshot runs.

**Tech Stack:** Go 1.26, cobra CLI, gopkg.in/yaml.v3, testify for tests

**Spec:** `docs/superpowers/specs/2026-04-24-named-locations-design.md`

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `pkg/config/config.go` | Modify | Add `Location` struct, update `Defaults`/`Snapshot`, new resolution helpers |
| `pkg/config/config_test.go` | Create | Tests for config parsing, resolution, YAML unmarshaling |
| `pkg/restic/restic.go` | Modify | Replace `UseLocal` with `Location`/`LocationName`, use resolution helpers |
| `pkg/restic/restic_test.go` | Create | Tests for `ResolvedRepo` and resolved settings |
| `pkg/metrics/metrics.go` | Modify | Add `Location` field to `Result`, include in labels |
| `pkg/metrics/metrics_test.go` | Create | Test that location label appears in pushed metrics |
| `cmd/backup/root.go` | Modify | Replace `--local` with `--location`, add `--init`, comma-split `--backup` |
| `config.yaml.example` | Modify | Update to named locations schema |
| `CLAUDE.md` | Modify | Update config documentation to reflect named locations |

---

### Task 1: Config structs and YAML parsing

**Files:**
- Modify: `pkg/config/config.go`
- Create: `pkg/config/config_test.go`

- [ ] **Step 1: Write test for new config YAML parsing**

Create `pkg/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_NamedLocations(t *testing.T) {
	yaml := `
defaults:
  defaultLocation: drobo
  retention: "6m"
  metricsURL: http://vmagent:8429
  timeout: "4h"
  locations:
    drobo:
      repoBase: /mnt/backup/
    dogutil:
      repoBase: /mnt/dogutil/
      limitUpload: 2048
      cacheDir: /tmp/restic-cache
      retention: "3m"
snapshots:
  d-immich:
    repoName: FAC35D351AFBAE75
    path:
      - /mnt/oatmeal/immich/library
      - /mnt/oatmeal/immich/backups
  d-paperless:
    repoName: 7AEF975276E4BB5B
    path: /mnt/oatmeal/paperless
    retention: "1m"
    limitUpload: 1024
`
	dir := t.TempDir()
	f := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(f, []byte(yaml), 0644))

	cfg, err := Load(f)
	require.NoError(t, err)

	// Defaults
	assert.Equal(t, "drobo", cfg.Defaults.DefaultLocation)
	assert.Equal(t, "6m", cfg.Defaults.Retention)
	assert.Equal(t, "http://vmagent:8429", cfg.Defaults.MetricsURL)
	assert.Equal(t, "4h", cfg.Defaults.Timeout)

	// Locations
	require.Len(t, cfg.Defaults.Locations, 2)

	drobo := cfg.Defaults.Locations["drobo"]
	require.NotNil(t, drobo)
	assert.Equal(t, "/mnt/backup/", drobo.RepoBase)
	assert.Equal(t, 0, drobo.LimitUpload)

	dogutil := cfg.Defaults.Locations["dogutil"]
	require.NotNil(t, dogutil)
	assert.Equal(t, "/mnt/dogutil/", dogutil.RepoBase)
	assert.Equal(t, 2048, dogutil.LimitUpload)
	assert.Equal(t, "/tmp/restic-cache", dogutil.CacheDir)
	assert.Equal(t, "3m", dogutil.Retention)

	// Snapshots
	require.Len(t, cfg.Snapshots, 2)

	immich := cfg.Snapshots["d-immich"]
	require.NotNil(t, immich)
	assert.Equal(t, "FAC35D351AFBAE75", immich.RepoName)
	assert.Equal(t, StringList{"/mnt/oatmeal/immich/library", "/mnt/oatmeal/immich/backups"}, immich.Path)

	paperless := cfg.Snapshots["d-paperless"]
	require.NotNil(t, paperless)
	assert.Equal(t, "7AEF975276E4BB5B", paperless.RepoName)
	assert.Equal(t, StringList{"/mnt/oatmeal/paperless"}, paperless.Path)
	assert.Equal(t, "1m", paperless.Retention)
	assert.Equal(t, 1024, paperless.LimitUpload)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/config/ -run TestLoad_NamedLocations -v`
Expected: FAIL — `Defaults` struct doesn't have `DefaultLocation` or `Locations` fields.

- [ ] **Step 3: Update config structs**

In `pkg/config/config.go`, replace the `Defaults` and `Snapshot` structs and update `ResolvedRepo`:

```go
// Defaults holds default values used by --new and backup operations
type Defaults struct {
	DefaultLocation string               `yaml:"defaultLocation,omitempty"`
	Retention       string               `yaml:"retention,omitempty"`
	MetricsURL      string               `yaml:"metricsURL,omitempty"`
	Timeout         string               `yaml:"timeout,omitempty"`
	Locations       map[string]*Location  `yaml:"locations,omitempty"`
}

// Location defines a backup destination and its settings
type Location struct {
	RepoBase    string `yaml:"repoBase"`
	LimitUpload int    `yaml:"limitUpload,omitempty"`
	CacheDir    string `yaml:"cacheDir,omitempty"`
	Retention   string `yaml:"retention,omitempty"`
}

// Snapshot defines a single backup target
type Snapshot struct {
	RepoName    string     `yaml:"repoName,omitempty"`
	Path        StringList `yaml:"path,omitempty"`
	Exclude     string     `yaml:"exclude,omitempty"`
	Retention   string     `yaml:"retention,omitempty"`
	LimitUpload int        `yaml:"limitUpload,omitempty"`
	CacheDir    string     `yaml:"cacheDir,omitempty"`
}

// ResolvedRepo returns the full repo path for the given location
func (s *Snapshot) ResolvedRepo(loc *Location) string {
	if s.RepoName != "" && loc != nil && loc.RepoBase != "" {
		return filepath.Join(loc.RepoBase, s.RepoName)
	}
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/config/ -run TestLoad_NamedLocations -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): add Location struct and named locations parsing"
```

---

### Task 2: Three-tier resolution helpers

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `pkg/config/config_test.go`

- [ ] **Step 1: Write tests for resolution helpers**

Append to `pkg/config/config_test.go`:

```go
func TestResolvedRepo(t *testing.T) {
	loc := &Location{RepoBase: "/mnt/backup/"}
	snap := &Snapshot{RepoName: "FAC35D351AFBAE75"}
	assert.Equal(t, "/mnt/backup/FAC35D351AFBAE75", snap.ResolvedRepo(loc))
}

func TestResolvedRepo_EmptyRepoName(t *testing.T) {
	loc := &Location{RepoBase: "/mnt/backup/"}
	snap := &Snapshot{}
	assert.Equal(t, "", snap.ResolvedRepo(loc))
}

func TestResolvedRepo_NilLocation(t *testing.T) {
	snap := &Snapshot{RepoName: "FAC35D351AFBAE75"}
	assert.Equal(t, "", snap.ResolvedRepo(nil))
}

func TestResolvedLimitUpload(t *testing.T) {
	tests := []struct {
		name     string
		snap     *Snapshot
		loc      *Location
		expected int
	}{
		{
			name:     "snapshot overrides location",
			snap:     &Snapshot{LimitUpload: 512},
			loc:      &Location{LimitUpload: 2048},
			expected: 512,
		},
		{
			name:     "falls back to location",
			snap:     &Snapshot{},
			loc:      &Location{LimitUpload: 2048},
			expected: 2048,
		},
		{
			name:     "zero when neither set",
			snap:     &Snapshot{},
			loc:      &Location{},
			expected: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.snap.ResolvedLimitUpload(tt.loc))
		})
	}
}

func TestResolvedCacheDir(t *testing.T) {
	tests := []struct {
		name     string
		snap     *Snapshot
		loc      *Location
		expected string
	}{
		{
			name:     "snapshot overrides location",
			snap:     &Snapshot{CacheDir: "/snap-cache"},
			loc:      &Location{CacheDir: "/loc-cache"},
			expected: "/snap-cache",
		},
		{
			name:     "falls back to location",
			snap:     &Snapshot{},
			loc:      &Location{CacheDir: "/loc-cache"},
			expected: "/loc-cache",
		},
		{
			name:     "empty when neither set",
			snap:     &Snapshot{},
			loc:      &Location{},
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.snap.ResolvedCacheDir(tt.loc))
		})
	}
}

func TestResolvedRetention(t *testing.T) {
	tests := []struct {
		name     string
		snap     *Snapshot
		loc      *Location
		defaults Defaults
		expected string
	}{
		{
			name:     "snapshot overrides all",
			snap:     &Snapshot{Retention: "1m"},
			loc:      &Location{Retention: "3m"},
			defaults: Defaults{Retention: "6m"},
			expected: "1m",
		},
		{
			name:     "location overrides global",
			snap:     &Snapshot{},
			loc:      &Location{Retention: "3m"},
			defaults: Defaults{Retention: "6m"},
			expected: "3m",
		},
		{
			name:     "falls back to global",
			snap:     &Snapshot{},
			loc:      &Location{},
			defaults: Defaults{Retention: "6m"},
			expected: "6m",
		},
		{
			name:     "empty when nothing set",
			snap:     &Snapshot{},
			loc:      &Location{},
			defaults: Defaults{},
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.snap.ResolvedRetention(tt.loc, tt.defaults))
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/config/ -v`
Expected: FAIL — `ResolvedLimitUpload`, `ResolvedCacheDir`, `ResolvedRetention` undefined.

- [ ] **Step 3: Implement resolution helpers**

Add to `pkg/config/config.go`:

```go
// ResolvedLimitUpload returns the effective upload limit.
// Resolution: snapshot -> location.
func (s *Snapshot) ResolvedLimitUpload(loc *Location) int {
	if s.LimitUpload > 0 {
		return s.LimitUpload
	}
	if loc != nil {
		return loc.LimitUpload
	}
	return 0
}

// ResolvedCacheDir returns the effective cache directory.
// Resolution: snapshot -> location.
func (s *Snapshot) ResolvedCacheDir(loc *Location) string {
	if s.CacheDir != "" {
		return s.CacheDir
	}
	if loc != nil {
		return loc.CacheDir
	}
	return ""
}

// ResolvedRetention returns the effective retention period.
// Resolution: snapshot -> location -> global default.
func (s *Snapshot) ResolvedRetention(loc *Location, defaults Defaults) string {
	if s.Retention != "" {
		return s.Retention
	}
	if loc != nil && loc.Retention != "" {
		return loc.Retention
	}
	return defaults.Retention
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/config/ -v`
Expected: PASS — all resolution tests green.

- [ ] **Step 5: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): add three-tier resolution helpers for settings"
```

---

### Task 3: Add location label to metrics

**Files:**
- Modify: `pkg/metrics/metrics.go`
- Create: `pkg/metrics/metrics_test.go`

- [ ] **Step 1: Write test for location label in metrics**

Create `pkg/metrics/metrics_test.go`:

```go
package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPush_IncludesLocationLabel(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := &Pusher{URL: srv.URL}
	p.Push(Result{
		Snapshot: "d-immich",
		Location: "drobo",
		Success:  true,
		Duration: 60 * time.Second,
	})

	assert.Contains(t, body, `snapshot="d-immich"`)
	assert.Contains(t, body, `location="drobo"`)
	assert.Contains(t, body, `vrestic_backup_success`)
	assert.Contains(t, body, `vrestic_backup_duration_seconds`)
	assert.Contains(t, body, `vrestic_backup_last_run_timestamp`)
	assert.Contains(t, body, `vrestic_backup_last_success_timestamp`)
}

func TestPush_EmptyURL_NoOp(t *testing.T) {
	p := &Pusher{URL: ""}
	// Should not panic or make any HTTP calls
	p.Push(Result{Snapshot: "test", Location: "drobo", Success: true})
}

func TestPush_FailureOmitsSuccessTimestamp(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := &Pusher{URL: srv.URL}
	p.Push(Result{
		Snapshot: "d-immich",
		Location: "drobo",
		Success:  false,
		Duration: 10 * time.Second,
	})

	assert.Contains(t, body, `vrestic_backup_success`)
	assert.NotContains(t, body, `vrestic_backup_last_success_timestamp`)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/metrics/ -v`
Expected: FAIL — `Result` has no `Location` field, and labels don't include `location`.

- [ ] **Step 3: Add location to Result and metric labels**

In `pkg/metrics/metrics.go`, add `Location` field to `Result` and update the format strings:

Change `Result` struct to:

```go
// Result holds the outcome of a single backup run
type Result struct {
	Snapshot string
	Location string
	Success  bool
	Duration time.Duration
	Error    error
}
```

Change the four format strings in `Push` to include the location label. Replace the lines block with:

```go
	var lines []string

	// Success gauge (1 = success, 0 = failure)
	lines = append(lines,
		fmt.Sprintf(`vrestic_backup_success{snapshot=%q,location=%q,job=%q} %d %d`, r.Snapshot, r.Location, job, successVal, now*1000),
	)

	// Duration in seconds
	lines = append(lines,
		fmt.Sprintf(`vrestic_backup_duration_seconds{snapshot=%q,location=%q,job=%q} %.2f %d`, r.Snapshot, r.Location, job, r.Duration.Seconds(), now*1000),
	)

	// Last run timestamp
	lines = append(lines,
		fmt.Sprintf(`vrestic_backup_last_run_timestamp{snapshot=%q,location=%q,job=%q} %d %d`, r.Snapshot, r.Location, job, now, now*1000),
	)

	// Last success timestamp (only on success)
	if r.Success {
		lines = append(lines,
			fmt.Sprintf(`vrestic_backup_last_success_timestamp{snapshot=%q,location=%q,job=%q} %d %d`, r.Snapshot, r.Location, job, now, now*1000),
		)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/metrics/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add pkg/metrics/metrics.go pkg/metrics/metrics_test.go
git commit -m "feat(metrics): add location label to all pushed metrics"
```

---

### Task 4: Update Runner to use Location

**Files:**
- Modify: `pkg/restic/restic.go`

- [ ] **Step 1: Verify the project compiles before changes**

Run: `cd /home/thorix/git/thorix/vrestic && go build ./...`
Expected: FAIL — `cmd/backup/root.go` references old `Defaults` fields and `UseLocal`. This is expected since we changed the config structs in Task 1 but haven't updated the consumers yet. We'll update `restic.go` first, then `root.go` in Task 5 to fix compilation.

- [ ] **Step 2: Update Runner struct and Run method**

In `pkg/restic/restic.go`, replace the `Runner` struct and update `Run`, `Unlock`, and `ListSnapshots`:

Replace `Runner` struct:

```go
// Runner orchestrates restic backup operations
type Runner struct {
	DryRun       bool
	Verbose      bool
	Location     *config.Location
	LocationName string
	Vault        vault.Vault
	Defaults     config.Defaults
	Metrics      *metrics.Pusher
	Timeout      time.Duration
	ctx          context.Context
}
```

In `RunTimed`, update the metrics push to include location:

```go
	if r.Metrics != nil {
		r.Metrics.Push(metrics.Result{
			Snapshot: snapshotName,
			Location: r.LocationName,
			Success:  err == nil,
			Duration: duration,
			Error:    err,
		})
	}
```

In `Run`, replace the repo resolution and settings lookups. Change:

```go
	repoPath := snap.ResolvedRepo(r.Defaults, r.UseLocal)
```

To:

```go
	repoPath := snap.ResolvedRepo(r.Location)
```

Replace the `limitUpload` block:

```go
	limitUpload := snap.ResolvedLimitUpload(r.Location)
	if limitUpload > 0 && isRemoteRepo(repoPath) {
		args = append(args, "--limit-upload", strconv.Itoa(limitUpload))
	} else if limitUpload > 0 {
		slog.Info("Skipping limitUpload for local repo", "snapshot", snapshotName, "repo", repoPath)
	}
```

Replace the `cacheDir` references in `Run`. Change:

```go
	if len(snap.CacheDir) != 0 {
		args = append(args, "--cache-dir", snap.CacheDir)
	}
```

To:

```go
	cacheDir := snap.ResolvedCacheDir(r.Location)
	if cacheDir != "" {
		args = append(args, "--cache-dir", cacheDir)
	}
```

And the same for the cacheDir in the unlock and forget sections of `Run`. Find:

```go
	if len(snap.CacheDir) != 0 {
		unlockArgs = append(unlockArgs, "--cache-dir", snap.CacheDir)
	}
```

Replace with:

```go
	if cacheDir != "" {
		unlockArgs = append(unlockArgs, "--cache-dir", cacheDir)
	}
```

And for forget args, find:

```go
	if len(snap.CacheDir) != 0 {
		forgetArgs = append(forgetArgs, "--cache-dir", snap.CacheDir)
	}
```

Replace with:

```go
	if cacheDir != "" {
		forgetArgs = append(forgetArgs, "--cache-dir", cacheDir)
	}
```

Replace the retention block:

```go
	retention := snap.Retention
	if retention == "" {
		retention = r.Defaults.Retention
	}
	if retention == "" {
		retention = "1m"
	}
```

With:

```go
	retention := snap.ResolvedRetention(r.Location, r.Defaults)
	if retention == "" {
		retention = "1m"
	}
```

In `Unlock`, change:

```go
	repoPath := snap.ResolvedRepo(r.Defaults, r.UseLocal)
```

To:

```go
	repoPath := snap.ResolvedRepo(r.Location)
```

And replace `snap.CacheDir` references in `Unlock`:

```go
	if len(snap.CacheDir) != 0 {
		args = append(args, "--cache-dir", snap.CacheDir)
	}
```

With:

```go
	cacheDir := snap.ResolvedCacheDir(r.Location)
	if cacheDir != "" {
		args = append(args, "--cache-dir", cacheDir)
	}
```

In `ListSnapshots`, change:

```go
	repoPath := snap.ResolvedRepo(r.Defaults, r.UseLocal)
```

To:

```go
	repoPath := snap.ResolvedRepo(r.Location)
```

- [ ] **Step 3: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add pkg/restic/restic.go
git commit -m "feat(restic): replace UseLocal with Location in Runner"
```

---

### Task 5: Update CLI — replace --local with --location, add comma-split --backup, add --init

**Files:**
- Modify: `cmd/backup/root.go`

- [ ] **Step 1: Update opts struct and flag registration**

Replace the opts struct:

```go
var opts struct {
	debug         bool
	dryRun        bool
	quiet         bool
	listSnapshots bool
	runAll        bool
	generateName  bool
	unlock        bool
	unlockRemove  bool
	syncConfig    bool
	location      string
	newSnapshot   string
	initSnapshot  string
	runSnapshot   string
	configFile    string
}
```

In `init()`, remove the `--local` flag line and add the new flags. Replace:

```go
	rootCmd.Flags().BoolVar(&opts.useLocal, "local", false, "Use local repo for restic")
```

With:

```go
	rootCmd.Flags().StringVar(&opts.location, "location", "", "Backup location name (defaults to config's defaultLocation)")
```

Add the `--init` flag after the `--new` flag:

```go
	rootCmd.Flags().StringVar(&opts.initSnapshot, "init", "", "Initialize restic repo for an existing snapshot on a location")
```

- [ ] **Step 2: Add location resolution helper**

Add a helper function in `cmd/backup/root.go` after the `init()` function:

```go
// resolveLocation looks up the named location from config. If name is empty,
// uses the config's defaultLocation.
func resolveLocation(cfg *config.Config, name string) (string, *config.Location, error) {
	if name == "" {
		name = cfg.Defaults.DefaultLocation
	}
	if name == "" {
		return "", nil, fmt.Errorf("no location specified and no defaultLocation in config")
	}
	loc, ok := cfg.Defaults.Locations[name]
	if !ok {
		available := make([]string, 0, len(cfg.Defaults.Locations))
		for k := range cfg.Defaults.Locations {
			available = append(available, k)
		}
		sort.Strings(available)
		return "", nil, fmt.Errorf("location %q not found in config (available: %s)", name, strings.Join(available, ", "))
	}
	return name, loc, nil
}
```

- [ ] **Step 3: Update RunE to use location and comma-split backup**

In the `RunE` function, after config is loaded and before Vault init, add location resolution. Replace the section from `// List doesn't need Vault` through the `runner` construction and all the way to the end of `RunE` with:

```go
		// List doesn't need Vault
		if opts.listSnapshots {
			displayList(cfg)
			return nil
		}

		// Resolve location (needed for most operations)
		locName, loc, err := resolveLocation(cfg, opts.location)
		if err != nil {
			return err
		}

		// Initialize vault
		var v vault.Vault
		if err := v.New(); err != nil {
			return err
		}
		if err := v.Connect(); err != nil {
			return err
		}

		// Sync config from Vault
		if opts.syncConfig {
			return syncConfig(&v, opts.configFile)
		}

		// Create new snapshot
		if opts.newSnapshot != "" {
			return runNew(opts.newSnapshot, cfg, &v, opts.configFile, locName, loc)
		}

		// Init existing snapshot on a location
		if opts.initSnapshot != "" {
			return runInit(opts.initSnapshot, cfg, &v, locName, loc)
		}

		var timeout time.Duration
		if cfg.Defaults.Timeout != "" {
			timeout, _ = time.ParseDuration(cfg.Defaults.Timeout)
		}

		runner := &restic.Runner{
			DryRun:       opts.dryRun,
			Verbose:      !opts.quiet,
			Location:     loc,
			LocationName: locName,
			Vault:        v,
			Defaults:     cfg.Defaults,
			Timeout:      timeout,
		}
		if cfg.Defaults.MetricsURL != "" {
			runner.Metrics = &metrics.Pusher{URL: cfg.Defaults.MetricsURL}
		}

		if opts.unlock {
			if opts.runSnapshot != "" {
				names := splitSnapshots(opts.runSnapshot)
				for _, name := range names {
					snap, ok := cfg.Snapshots[name]
					if !ok {
						return fmt.Errorf("snapshot %q not found in config", name)
					}
					if err := runner.Unlock(name, snap, opts.unlockRemove); err != nil {
						return err
					}
				}
				return nil
			}
			if opts.runAll {
				slog.Info("Unlocking all repositories")
				var errs []error
				for name, snap := range cfg.Snapshots {
					if err := runner.Unlock(name, snap, opts.unlockRemove); err != nil {
						slog.Error("Unlock failed", "snapshot", name, "error", err)
						errs = append(errs, fmt.Errorf("%s: %w", name, err))
					}
				}
				if len(errs) > 0 {
					return fmt.Errorf("%d unlock(s) failed", len(errs))
				}
				return nil
			}
			return errors.New("--unlock requires either --backup <name> or --all")
		}

		if opts.runSnapshot != "" {
			names := splitSnapshots(opts.runSnapshot)
			// Validate all names exist before running any
			for _, name := range names {
				if _, ok := cfg.Snapshots[name]; !ok {
					return fmt.Errorf("snapshot %q not found in config", name)
				}
			}
			var errs []error
			for _, name := range names {
				snap := cfg.Snapshots[name]
				if err := runner.RunTimed(name, snap); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
				}
			}
			if len(errs) > 0 {
				return fmt.Errorf("%d backup(s) failed", len(errs))
			}
			return nil
		}

		if opts.runAll {
			slog.Info("Running all backups")
			var errs []error
			for name, snap := range cfg.Snapshots {
				if err := runner.RunTimed(name, snap); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
				}
			}
			if len(errs) > 0 {
				return fmt.Errorf("%d backup(s) failed", len(errs))
			}
			return nil
		}

		return cmd.Help()
```

- [ ] **Step 4: Add splitSnapshots helper**

Add after `resolveLocation`:

```go
// splitSnapshots splits a comma-separated snapshot list and trims whitespace.
func splitSnapshots(input string) []string {
	parts := strings.Split(input, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			names = append(names, p)
		}
	}
	return names
}
```

- [ ] **Step 5: Update displayList to use default location**

Replace `displayList`:

```go
func displayList(cfg *config.Config) {
	keys := make([]string, 0, len(cfg.Snapshots))
	for k := range cfg.Snapshots {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Use default location for display
	var loc *config.Location
	if cfg.Defaults.DefaultLocation != "" {
		loc = cfg.Defaults.Locations[cfg.Defaults.DefaultLocation]
	}

	w := tabwriter.NewWriter(os.Stdout, 10, 8, 2, ' ', 0)
	fmt.Fprintf(w, "SNAPSHOT\tPATH\tREPO (%s)\n", cfg.Defaults.DefaultLocation)
	for _, name := range keys {
		s := cfg.Snapshots[name]
		repo := s.ResolvedRepo(loc)
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, strings.Join(s.Path, ", "), repo)
	}
	w.Flush()
}
```

- [ ] **Step 6: Update runNew to accept location parameters**

Change `runNew` signature and update the body. Replace the full function:

```go
func runNew(name string, cfg *config.Config, v *vault.Vault, configFile string, locName string, loc *config.Location) error {
	fmt.Printf("\nCreating new backup snapshot: %s (location: %s)\n\n", name, locName)

	// Check if snapshot already exists in local config
	if _, exists := cfg.Snapshots[name]; exists {
		return fmt.Errorf("snapshot %q already exists in local config", name)
	}

	// Check Vault for existing password
	fmt.Print("Checking Vault for existing password... ")
	exists, err := v.PasswordExists(name)
	if err != nil {
		return fmt.Errorf("checking Vault: %w", err)
	}
	if exists {
		fmt.Println("FOUND")
		fmt.Println("\n  Password already exists in Vault — this repo may already be in use.")
		fmt.Println("  Run 'vrestic --sync-config' to pull the latest config from Vault.")
		return errors.New("refusing to overwrite existing Vault password")
	}
	fmt.Println("not found (OK)")

	reader := bufio.NewReader(os.Stdin)

	// Prompt for source paths
	fmt.Println()
	pathInput := promptLine(reader, "Source path(s) to back up (comma-separated)", "")
	if pathInput == "" {
		return errors.New("at least one source path is required")
	}

	var paths []string
	for _, p := range strings.Split(pathInput, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("path %q: %w", p, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("path %q is not a directory", p)
		}
		fmt.Printf("  ✓ %s exists\n", p)
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return errors.New("no valid paths provided")
	}

	// Generate random repo name
	repoName, err := restic.PseudoName()
	if err != nil {
		return fmt.Errorf("generating repo name: %w", err)
	}

	repoPath := filepath.Join(loc.RepoBase, repoName)
	if info, err := os.Stat(loc.RepoBase); err != nil {
		fmt.Printf("  ⚠ %s not accessible locally (OK if it's a remote mount)\n", loc.RepoBase)
	} else if !info.IsDir() {
		return fmt.Errorf("repo base %q is not a directory", loc.RepoBase)
	} else {
		if _, err := os.Stat(repoPath); err == nil {
			return fmt.Errorf("repo path %q already exists (collision)", repoPath)
		}
		fmt.Printf("  ✓ %s accessible, no collision\n", loc.RepoBase)
	}
	fmt.Printf("  Generated repo: %s\n", repoPath)

	// Prompt for optional settings
	fmt.Println()
	defaultRetention := cfg.Defaults.Retention
	if defaultRetention == "" {
		defaultRetention = "1m"
	}
	retention := promptLine(reader, "Retention period", defaultRetention)

	// Build snapshot
	snap := &config.Snapshot{
		RepoName: repoName,
		Path:     config.StringList(paths),
	}
	if retention != defaultRetention {
		snap.Retention = retention
	}

	// Summary
	fmt.Printf("\nSummary:\n")
	fmt.Printf("  Snapshot:  %s\n", name)
	fmt.Printf("  Location:  %s\n", locName)
	fmt.Printf("  Path:      %s\n", strings.Join(paths, ", "))
	fmt.Printf("  Repo:      %s\n", repoPath)
	if snap.Retention != "" {
		fmt.Printf("  Retention: %s\n", snap.Retention)
	}
	fmt.Println()

	// Add to config and write
	if cfg.Snapshots == nil {
		cfg.Snapshots = make(map[string]*config.Snapshot)
	}
	cfg.Snapshots[name] = snap

	fmt.Printf("Writing %s... ", configFile)
	if err := cfg.WriteConf(configFile); err != nil {
		return err
	}
	fmt.Println("done")

	// Generate and write password to Vault
	password := restic.CreatePassword()
	fmt.Printf("Writing password to Vault (kv/vrestic/passwords/%s)... ", name)
	if err := v.WritePassword(name, password); err != nil {
		return err
	}
	fmt.Println("done")

	// Initialize the restic repo
	if _, err := os.Stat(loc.RepoBase); err != nil {
		fmt.Printf("\nRepo base not accessible locally — skip init (run --init on the target host)\n")
	} else {
		fmt.Printf("Initializing restic repo at %s... ", repoPath)
		env := append(os.Environ(), "RESTIC_PASSWORD="+password)
		if err := os.MkdirAll(repoPath, 0755); err != nil {
			return fmt.Errorf("creating repo directory: %w", err)
		}
		initErr := shell.Run(shell.Command{
			Binary: "restic",
			Args:   []string{"-r", repoPath, "init"},
			Envs:   env,
		})
		if initErr != nil {
			return fmt.Errorf("restic init failed: %w", initErr)
		}
		fmt.Println("done")
	}

	// Upload config to Vault
	configData, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("reading config for upload: %w", err)
	}
	fmt.Print("Uploading config to Vault (kv/vrestic/config)... ")
	if err := v.WriteConfig(string(configData)); err != nil {
		return err
	}
	fmt.Println("done")

	fmt.Printf("\nReady! Run your first backup with:\n")
	fmt.Printf("  vrestic --backup %s\n", name)
	return nil
}
```

- [ ] **Step 7: Add runInit function**

Add after `runNew`:

```go
func runInit(name string, cfg *config.Config, v *vault.Vault, locName string, loc *config.Location) error {
	fmt.Printf("\nInitializing repo for snapshot %s on location %s\n\n", name, locName)

	// Snapshot must already exist
	snap, ok := cfg.Snapshots[name]
	if !ok {
		return fmt.Errorf("snapshot %q not found in config (use --new to create a new snapshot)", name)
	}

	if snap.RepoName == "" {
		return fmt.Errorf("snapshot %q has no repoName", name)
	}

	repoPath := snap.ResolvedRepo(loc)
	if repoPath == "" {
		return fmt.Errorf("cannot resolve repo path for %q on location %q", name, locName)
	}

	// Check if repo already exists
	if info, err := os.Stat(repoPath); err == nil && info.IsDir() {
		return fmt.Errorf("repo path %q already exists", repoPath)
	}

	// Read existing password from Vault
	fmt.Print("Reading password from Vault... ")
	password, err := v.ReadPassword(name)
	if err != nil {
		return fmt.Errorf("reading password: %w (does the snapshot exist in Vault?)", err)
	}
	fmt.Println("done")

	// Initialize
	fmt.Printf("Initializing restic repo at %s... ", repoPath)
	env := append(os.Environ(), "RESTIC_PASSWORD="+password)
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		return fmt.Errorf("creating repo directory: %w", err)
	}
	initErr := shell.Run(shell.Command{
		Binary: "restic",
		Args:   []string{"-r", repoPath, "init"},
		Envs:   env,
	})
	if initErr != nil {
		return fmt.Errorf("restic init failed: %w", initErr)
	}
	fmt.Println("done")

	fmt.Printf("\nReady! Run backup with:\n")
	fmt.Printf("  vrestic --backup %s --location %s\n", name, locName)
	return nil
}
```

- [ ] **Step 8: Verify compilation**

Run: `cd /home/thorix/git/thorix/vrestic && go build ./...`
Expected: PASS — compiles cleanly.

- [ ] **Step 9: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add cmd/backup/root.go
git commit -m "feat(cli): replace --local with --location, add --init, comma-split --backup"
```

---

### Task 6: Update config example and CLAUDE.md

**Files:**
- Modify: `config.yaml.example`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update config.yaml.example**

Replace the full contents of `config.yaml.example`:

```yaml
# vrestic configuration example
# Passwords are stored in Vault at kv/vrestic/passwords/<snapshot-name>

defaults:
  # Which location to use when --location is not specified
  defaultLocation: local

  # Global retention default (restic duration format: 1y2m3d)
  retention: "6m"

  # Prometheus push endpoint for backup metrics (empty = disabled)
  metricsURL: http://vmagent:8429/api/v1/import/prometheus

  # Per-snapshot timeout (Go duration format)
  timeout: "4h"

  # Named backup destinations
  locations:
    local:
      repoBase: /mnt/backup/
    remote:
      repoBase: sftp:backuphost:/mnt/backups/
      limitUpload: 2048          # KiB/s, only applied to remote repos
      cacheDir: /tmp/restic-cache
      retention: "3m"            # Override global retention for this location

snapshots:
  documents:
    repoName: A1B2C3D4E5F67890
    path: /data/documents
    exclude: "*.tmp"

  photos:
    repoName: 1234567890ABCDEF
    path: /data/photos
    retention: "1y"              # Override retention for this snapshot

  database:
    repoName: FEDCBA0987654321
    path: /data/database
    retention: "3m"
```

- [ ] **Step 2: Update CLAUDE.md config section**

In `CLAUDE.md`, replace the `## Config Design` section with:

```markdown
## Config Design

The config uses named locations for backup destinations and a `defaults` block to avoid repetition:

\```yaml
defaults:
  defaultLocation: drobo
  retention: "6m"
  metricsURL: http://vmagent:8429/api/v1/import/prometheus
  timeout: "4h"
  locations:
    drobo:
      repoBase: /mnt/backup/
    dogutil:
      repoBase: /mnt/dogutil/
      limitUpload: 2048

snapshots:
  d-immich:
    repoName: FAC35D351AFBAE75
    path:
      - /mnt/oatmeal/immich/library
\```

Path resolution: `snap.ResolvedRepo(location)` joins `location.RepoBase` + `repoName`.

Setting resolution order: snapshot -> location -> global default.
- `limitUpload`, `cacheDir`: snapshot -> location (no global default)
- `retention`: snapshot -> location -> global default
- `limitUpload` is automatically skipped for local filesystem repos
```

Also in `CLAUDE.md`, replace the `## Key Design Decisions` section to remove references to `--local`:

```markdown
## Key Design Decisions

- **Vault is authoritative**: `--new` refuses to overwrite existing Vault passwords
- **CronJob is read-only**: No auto-create of passwords or repos in cron mode
- **Local `--new` handles creation**: Interactive prompts, restic init, Vault writes, config upload
- **`--init` for additional locations**: Inits an existing snapshot on a new location using the existing Vault password
- **Named locations**: `--location <name>` selects backup destination; defaults to `defaults.defaultLocation`
- **File-based fallback**: `RESTIC_SECRETS_DIR` env var skips Vault entirely (reads passwords from mounted files)
- **Comma-separated backups**: `--backup a,b,c` runs multiple snapshots sequentially with fail-fast validation
```

Also update the `## Common Workflows` section:

```markdown
## Common Workflows

### Adding a new backup
\```bash
go run ./ --new d-mybackup --in config.yaml
# Follow prompts, then:
go run ./ --backup d-mybackup --in config.yaml
\```

### Initializing a snapshot on another location
\```bash
go run ./ --init d-mybackup --location dogutil --in config.yaml
go run ./ --backup d-mybackup --location dogutil --in config.yaml
\```

### Backing up multiple snapshots
\```bash
go run ./ --backup d-movies,d-photos,d-music --location drobo
\```

### Syncing config from Vault
\```bash
go run ./ --sync-config --in config.yaml
\```
```

- [ ] **Step 3: Verify compilation still passes**

Run: `cd /home/thorix/git/thorix/vrestic && go build ./...`
Expected: PASS

- [ ] **Step 4: Run all tests**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./... -v`
Expected: PASS — all config and metrics tests green.

- [ ] **Step 5: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add config.yaml.example CLAUDE.md
git commit -m "docs: update config example and CLAUDE.md for named locations"
```

---

### Task 7: End-to-end dry-run verification

**Files:** None (manual verification)

- [ ] **Step 1: Create a test config file**

Write `/tmp/vrestic-test-config.yaml`:

```yaml
defaults:
  defaultLocation: drobo
  retention: "6m"
  timeout: "4h"
  locations:
    drobo:
      repoBase: /mnt/backup/
    dogutil:
      repoBase: /mnt/dogutil/
      limitUpload: 2048
snapshots:
  d-immich:
    repoName: FAC35D351AFBAE75
    path:
      - /mnt/oatmeal/immich/library
      - /mnt/oatmeal/immich/backups
  d-photos:
    repoName: 14F3EB97C4E93DAE
    path:
      - /mnt/oatmeal/photos
```

- [ ] **Step 2: Test --list**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --list --in /tmp/vrestic-test-config.yaml`
Expected: Table showing snapshots with REPO column showing `/mnt/backup/FAC35D351AFBAE75` etc., header says `REPO (drobo)`.

- [ ] **Step 3: Test --dry-run with default location**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --dry-run --backup d-immich --in /tmp/vrestic-test-config.yaml`
Expected: Dry run output showing `restic -r /mnt/backup/FAC35D351AFBAE75 backup ...`

- [ ] **Step 4: Test --dry-run with explicit location**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --dry-run --backup d-immich --location dogutil --in /tmp/vrestic-test-config.yaml`
Expected: Dry run output showing `restic -r /mnt/dogutil/FAC35D351AFBAE75 backup ...`

- [ ] **Step 5: Test comma-separated --backup**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --dry-run --backup d-immich,d-photos --in /tmp/vrestic-test-config.yaml`
Expected: Dry run output for both snapshots sequentially.

- [ ] **Step 6: Test invalid location**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --dry-run --backup d-immich --location nonexistent --in /tmp/vrestic-test-config.yaml`
Expected: Error: `location "nonexistent" not found in config (available: dogutil, drobo)`

- [ ] **Step 7: Test invalid snapshot name in comma list**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --dry-run --backup d-immich,d-fake --in /tmp/vrestic-test-config.yaml`
Expected: Error: `snapshot "d-fake" not found in config` — fails before running any backup.

- [ ] **Step 8: Commit (no code changes, just verification)**

No commit needed — this task is verification only.

---

### Task 8: Migrate live config.yaml

**Files:**
- Modify: `config.yaml`

- [ ] **Step 1: Update config.yaml to named locations format**

Replace `config.yaml` contents with:

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
snapshots:
    d-audiobooks:
        repoName: C8FAC35E44D5A21A
        path:
            - /mnt/oatmeal/media/audio.books
    d-ebooks:
        repoName: 4BC1CCA5F50C31AF
        path:
            - /mnt/oatmeal/media/e-books
    d-immich:
        repoName: FAC35D351AFBAE75
        path:
            - /mnt/oatmeal/immich/library
            - /mnt/oatmeal/immich/backups
    d-movies:
        repoName: D604310CF8931665
        path:
            - /mnt/oatmeal/media/movies
    d-music:
        repoName: C21F184EEC48DC2C
        path:
            - /mnt/oatmeal/media/music
    d-paperless:
        repoName: 7AEF975276E4BB5B
        path:
            - /mnt/oatmeal/paperless
    d-photos:
        repoName: 14F3EB97C4E93DAE
        path:
            - /mnt/oatmeal/photos
    d-randr:
        repoName: 0F28F2EFFF6E48F1
        path:
            - /mnt/oatmeal/randr
    d-thorix:
        repoName: 596634E9AD4B1D81
        path:
            - /home/thorix/thorix-data2
    d-tvshows:
        repoName: 428F4D556A36CEC8
        path:
            - /mnt/oatmeal/media/tv.shows
```

- [ ] **Step 2: Verify with --list**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --list --in config.yaml`
Expected: All 10 snapshots listed with `/mnt/backup/<repoName>` paths, header says `REPO (drobo)`.

- [ ] **Step 3: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add config.yaml
git commit -m "chore: migrate config.yaml to named locations format"
```
