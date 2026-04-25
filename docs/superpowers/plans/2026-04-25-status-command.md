# Status Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `--status` flag to show backup health (last backup time, snapshot count, source path health, and optionally repo size) per snapshot on a location.

**Architecture:** New `shell.RunCapture` for capturing restic JSON output, new `Runner.Status` method for querying repo state, new `--status` flag in CLI that formats results as a table. Sequential restic calls per snapshot.

**Tech Stack:** Go 1.26, `encoding/json` for parsing restic output, existing tabwriter for formatting

**Spec:** `docs/superpowers/specs/2026-04-25-status-command-design.md`

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `pkg/shell/shell.go` | Modify | Add `RunCapture` — same as `Run` but captures stdout |
| `pkg/shell/shell_test.go` | Create | Test `RunCapture` |
| `pkg/restic/restic.go` | Modify | Add `StatusResult` struct, `Status` method, `checkSource` helper |
| `pkg/restic/restic_test.go` | Create | Test `checkSource`, `formatSize`, `parseSnapshotsJSON`, `parseStatsJSON` |
| `cmd/backup/root.go` | Modify | Add `--status` flag, status display logic |

---

### Task 1: Add RunCapture to shell package

**Files:**
- Modify: `pkg/shell/shell.go`
- Create: `pkg/shell/shell_test.go`

- [ ] **Step 1: Write test for RunCapture**

Create `pkg/shell/shell_test.go`:

```go
package shell

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCapture_Success(t *testing.T) {
	out, err := RunCapture(Command{
		Binary: "echo",
		Args:   []string{"hello world"},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world\n", string(out))
}

func TestRunCapture_Failure(t *testing.T) {
	_, err := RunCapture(Command{
		Binary: "false",
	})
	assert.Error(t, err)
}

func TestRunCapture_EmptyBinary(t *testing.T) {
	_, err := RunCapture(Command{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "No command specified")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/shell/ -v`
Expected: FAIL — `RunCapture` undefined.

- [ ] **Step 3: Implement RunCapture**

Add to `pkg/shell/shell.go` after the existing `Run` function:

```go
// RunCapture runs a shell command and returns its stdout as bytes.
// Stderr is still connected to the terminal for diagnostics.
func RunCapture(c Command) ([]byte, error) {
	if len(c.Binary) == 0 {
		return nil, errors.New("No command specified")
	}

	fullPathBinary, err := exec.LookPath(c.Binary)
	if err != nil {
		return nil, err
	}

	var cmd *exec.Cmd
	if c.Ctx != nil {
		cmd = exec.CommandContext(c.Ctx, fullPathBinary, c.Args...)
	} else {
		cmd = exec.Command(fullPathBinary, c.Args...)
	}
	cmd.Env = c.Envs
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		if c.Ctx != nil && c.Ctx.Err() != nil {
			return nil, fmt.Errorf("command timed out: %w", c.Ctx.Err())
		}
		if exiterr, ok := err.(*exec.ExitError); ok {
			if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				return nil, fmt.Errorf("shell command exit with code %d", status.ExitStatus())
			}
		}
		return nil, err
	}

	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/shell/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add pkg/shell/shell.go pkg/shell/shell_test.go
git commit -m "feat(shell): add RunCapture for capturing command stdout"
```

---

### Task 2: Add JSON parsing helpers and source check

**Files:**
- Modify: `pkg/restic/restic.go`
- Create: `pkg/restic/restic_test.go`

- [ ] **Step 1: Write tests for JSON parsing and source check**

Create `pkg/restic/restic_test.go`:

```go
package restic

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSnapshotsJSON(t *testing.T) {
	input := []byte(`[
		{"time":"2026-04-25T03:00:05.123Z","short_id":"ffddc6fe","hostname":"vrestic-123","paths":["/mnt/data"]},
		{"time":"2026-04-20T03:00:05.456Z","short_id":"a5cbbd33","hostname":"vrestic-456","paths":["/mnt/data"]}
	]`)
	count, latest, err := parseSnapshotsJSON(input)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, 2026, latest.Year())
	assert.Equal(t, time.Month(4), latest.Month())
	assert.Equal(t, 25, latest.Day())
}

func TestParseSnapshotsJSON_Empty(t *testing.T) {
	input := []byte(`[]`)
	count, latest, err := parseSnapshotsJSON(input)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
	assert.True(t, latest.IsZero())
}

func TestParseSnapshotsJSON_Null(t *testing.T) {
	input := []byte(`null`)
	count, latest, err := parseSnapshotsJSON(input)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
	assert.True(t, latest.IsZero())
}

func TestParseStatsJSON(t *testing.T) {
	input := []byte(`{"total_size":554984587264,"total_file_count":42301}`)
	size, err := parseStatsJSON(input)
	require.NoError(t, err)
	assert.Equal(t, int64(554984587264), size)
}

func TestParseStatsJSON_Zero(t *testing.T) {
	input := []byte(`{"total_size":0,"total_file_count":0}`)
	size, err := parseStatsJSON(input)
	require.NoError(t, err)
	assert.Equal(t, int64(0), size)
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
		{554984587264, "516.9 GiB"},
		{1099511627776, "1.0 TiB"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, FormatSize(tt.bytes))
		})
	}
}

func TestCheckSource(t *testing.T) {
	// Non-existent path
	result := checkSource([]string{"/nonexistent/path/that/does/not/exist"})
	assert.Equal(t, "NOT MOUNTED", result)
}

func TestCheckSource_Empty(t *testing.T) {
	dir := t.TempDir()
	result := checkSource([]string{dir})
	assert.Equal(t, "empty", result)
}

func TestCheckSource_OK(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeTestFile(t, dir, "testfile"))
	result := checkSource([]string{dir})
	assert.Equal(t, "ok", result)
}

func TestCheckSource_MixedPaths(t *testing.T) {
	dir := t.TempDir()
	result := checkSource([]string{dir, "/nonexistent/path"})
	assert.Equal(t, "NOT MOUNTED", result)
}

func writeTestFile(t *testing.T, dir, name string) error {
	t.Helper()
	return os.WriteFile(dir+"/"+name, []byte("test"), 0644)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/restic/ -v`
Expected: FAIL — undefined functions.

- [ ] **Step 3: Implement StatusResult, parsing helpers, checkSource, FormatSize**

Add to `pkg/restic/restic.go` (add `"encoding/json"` to imports):

```go
// StatusResult holds the status of a single snapshot on a location
type StatusResult struct {
	Snapshot      string
	Location      string
	LastBackup    time.Time // zero value = never
	SnapshotCount int
	TotalSize     int64  // bytes, -1 = unknown
	Source        string // "ok", "empty", "NOT MOUNTED"
	RepoState     string // "ok", "not initialized", "unreachable"
}

// resticSnapshot is the JSON structure returned by restic snapshots --json
type resticSnapshot struct {
	Time string `json:"time"`
}

// resticStats is the JSON structure returned by restic stats --json
type resticStats struct {
	TotalSize int64 `json:"total_size"`
}

func parseSnapshotsJSON(data []byte) (count int, latest time.Time, err error) {
	var snapshots []resticSnapshot
	if err := json.Unmarshal(data, &snapshots); err != nil {
		return 0, time.Time{}, fmt.Errorf("parsing snapshots JSON: %w", err)
	}
	if len(snapshots) == 0 {
		return 0, time.Time{}, nil
	}
	for _, s := range snapshots {
		t, err := time.Parse(time.RFC3339Nano, s.Time)
		if err != nil {
			continue
		}
		if t.After(latest) {
			latest = t
		}
	}
	return len(snapshots), latest, nil
}

func parseStatsJSON(data []byte) (int64, error) {
	var stats resticStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return 0, fmt.Errorf("parsing stats JSON: %w", err)
	}
	return stats.TotalSize, nil
}

// FormatSize formats bytes into a human-readable string.
func FormatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(bytes)
	for _, unit := range units {
		size /= 1024
		if size < 1024 || unit == "TiB" {
			return fmt.Sprintf("%.1f %s", size, unit)
		}
	}
	return fmt.Sprintf("%.1f TiB", size)
}

// checkSource checks if source paths are accessible and non-empty.
func checkSource(paths []string) string {
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return "NOT MOUNTED"
		}
		if !info.IsDir() {
			return "NOT MOUNTED"
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return "NOT MOUNTED"
		}
		if len(entries) == 0 {
			return "empty"
		}
	}
	return "ok"
}
```

- [ ] **Step 4: Add `os` import to test file**

The test file needs `"os"` imported for `os.WriteFile`. Add it to the import block:

```go
import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./pkg/restic/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add pkg/restic/restic.go pkg/restic/restic_test.go
git commit -m "feat(restic): add status helpers — JSON parsing, source check, size formatting"
```

---

### Task 3: Add Runner.Status method

**Files:**
- Modify: `pkg/restic/restic.go`

- [ ] **Step 1: Implement Status method**

Add to `pkg/restic/restic.go` after the `ListSnapshots` method:

```go
// Status queries the state of a snapshot's repo on the current location.
func (r *Runner) Status(snapshotName string, snap *config.Snapshot) StatusResult {
	result := StatusResult{
		Snapshot:  snapshotName,
		Location:  r.LocationName,
		TotalSize: -1,
	}

	// Check source paths
	result.Source = checkSource(snap.Path)

	// Resolve repo path
	repoPath := snap.ResolvedRepo(r.Location)
	if repoPath == "" {
		result.RepoState = "not initialized"
		return result
	}

	// Check if location base is accessible (for local paths)
	if r.Location != nil && !isRemoteRepo(r.Location.RepoBase) {
		if _, err := os.Stat(r.Location.RepoBase); err != nil {
			result.RepoState = "unreachable"
			return result
		}
	}

	// Get password
	password, err := r.getPassword(snapshotName)
	if err != nil {
		slog.Warn("Cannot read password", "snapshot", snapshotName, "error", err)
		result.RepoState = "unreachable"
		return result
	}

	env := os.Environ()
	env = append(env, "RESTIC_PASSWORD="+password)

	cacheDir := snap.ResolvedCacheDir(r.Location)

	// Run restic snapshots --json
	snapArgs := []string{"-r", repoPath, "snapshots", "--json"}
	if cacheDir != "" {
		snapArgs = append(snapArgs, "--cache-dir", cacheDir)
	}
	out, err := shell.RunCapture(shell.Command{
		Binary: "restic",
		Args:   snapArgs,
		Envs:   env,
	})
	if err != nil {
		slog.Debug("restic snapshots failed", "snapshot", snapshotName, "error", err)
		result.RepoState = "not initialized"
		return result
	}

	count, latest, err := parseSnapshotsJSON(out)
	if err != nil {
		slog.Warn("Failed to parse snapshots JSON", "snapshot", snapshotName, "error", err)
		result.RepoState = "unreachable"
		return result
	}

	result.RepoState = "ok"
	result.SnapshotCount = count
	result.LastBackup = latest

	// If verbose, get repo size
	if r.Verbose {
		statsArgs := []string{"-r", repoPath, "stats", "--json"}
		if cacheDir != "" {
			statsArgs = append(statsArgs, "--cache-dir", cacheDir)
		}
		out, err := shell.RunCapture(shell.Command{
			Binary: "restic",
			Args:   statsArgs,
			Envs:   env,
		})
		if err != nil {
			slog.Warn("restic stats failed", "snapshot", snapshotName, "error", err)
		} else {
			size, err := parseStatsJSON(out)
			if err != nil {
				slog.Warn("Failed to parse stats JSON", "snapshot", snapshotName, "error", err)
			} else {
				result.TotalSize = size
			}
		}
	}

	return result
}
```

- [ ] **Step 2: Verify build**

Run: `cd /home/thorix/git/thorix/vrestic && go build ./...`
Expected: PASS

- [ ] **Step 3: Run existing tests**

Run: `cd /home/thorix/git/thorix/vrestic && go test ./... -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add pkg/restic/restic.go
git commit -m "feat(restic): add Status method for querying snapshot health"
```

---

### Task 4: Add --status flag and display logic

**Files:**
- Modify: `cmd/backup/root.go`

- [ ] **Step 1: Add statusSnapshot to opts and register flag**

In the opts struct, add:

```go
statusSnapshot string
```

In `init()`, add after the `--init` flag:

```go
rootCmd.Flags().StringVar(&opts.statusSnapshot, "status", "", "Show backup status for snapshot(s) (name, comma-list, or \"all\")")
```

- [ ] **Step 2: Add status handling in RunE**

In RunE, after the `--list` block (before location resolution), add:

```go
		// Status check
		if opts.statusSnapshot != "" {
			locName, loc, err := resolveLocation(cfg, opts.location)
			if err != nil {
				return err
			}

			var v vault.Vault
			if err := v.New(); err != nil {
				return err
			}
			if err := v.Connect(); err != nil {
				return err
			}

			runner := &restic.Runner{
				Verbose:      !opts.quiet,
				Location:     loc,
				LocationName: locName,
				Vault:        v,
				Defaults:     cfg.Defaults,
			}

			var names []string
			if opts.statusSnapshot == "all" {
				for name := range cfg.Snapshots {
					names = append(names, name)
				}
				sort.Strings(names)
			} else {
				names = splitSnapshots(opts.statusSnapshot)
			}

			// Validate
			for _, name := range names {
				if _, ok := cfg.Snapshots[name]; !ok {
					return fmt.Errorf("snapshot %q not found in config", name)
				}
			}

			// Collect results
			var results []restic.StatusResult
			for _, name := range names {
				snap := cfg.Snapshots[name]
				results = append(results, runner.Status(name, snap))
			}

			displayStatus(results, !opts.quiet)
			return nil
		}
```

- [ ] **Step 3: Add displayStatus function**

Add after `displayList`:

```go
func displayStatus(results []restic.StatusResult, verbose bool) {
	w := tabwriter.NewWriter(os.Stdout, 10, 8, 2, ' ', 0)
	if verbose {
		fmt.Fprintln(w, "SNAPSHOT\tLOCATION\tLAST BACKUP\tSNAPSHOTS\tSIZE\tSOURCE")
	} else {
		fmt.Fprintln(w, "SNAPSHOT\tLOCATION\tLAST BACKUP\tSNAPSHOTS\tSOURCE")
	}

	for _, r := range results {
		lastBackup := "never"
		snapCount := "0"
		size := "-"

		switch r.RepoState {
		case "not initialized":
			lastBackup = "not initialized"
			snapCount = "-"
		case "unreachable":
			lastBackup = "unreachable"
			snapCount = "-"
		default:
			if !r.LastBackup.IsZero() {
				lastBackup = r.LastBackup.Local().Format("2006-01-02 15:04:05")
			}
			snapCount = fmt.Sprintf("%d", r.SnapshotCount)
			if r.TotalSize >= 0 {
				size = restic.FormatSize(r.TotalSize)
			}
		}

		if verbose {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Snapshot, r.Location, lastBackup, snapCount, size, r.Source)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Snapshot, r.Location, lastBackup, snapCount, r.Source)
		}
	}
	w.Flush()
}
```

- [ ] **Step 4: Verify build and tests**

Run: `cd /home/thorix/git/thorix/vrestic && go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/thorix/git/thorix/vrestic
git add cmd/backup/root.go
git commit -m "feat(cli): add --status flag for backup health overview"
```

---

### Task 5: End-to-end verification

**Files:** None (manual testing)

- [ ] **Step 1: Test --status all with test config**

Write `/tmp/vrestic-status-test.yaml`:

```yaml
defaults:
  defaultLocation: drobo
  retention: "6m"
  locations:
    drobo:
      repoBase: /mnt/backup/
    dogutil:
      repoBase: /mnt/dogutil/
snapshots:
  d-immich:
    repoName: FAC35D351AFBAE75
    path:
      - /mnt/oatmeal/immich/library
  d-photos:
    repoName: 14F3EB97C4E93DAE
    path:
      - /mnt/oatmeal/photos
```

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --status all --in /tmp/vrestic-status-test.yaml`
Expected: Table output showing status for both snapshots. Source column should show state of source paths. Repos may show as "not initialized" or actual data depending on what's on disk.

- [ ] **Step 2: Test --status with single snapshot**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --status d-immich --in /tmp/vrestic-status-test.yaml`
Expected: Single row for d-immich.

- [ ] **Step 3: Test --status with explicit location**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --status all --location dogutil --in /tmp/vrestic-status-test.yaml`
Expected: Table showing dogutil as location column.

- [ ] **Step 4: Test verbose mode**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --status all --in /tmp/vrestic-status-test.yaml` (without -q)
Expected: SIZE column appears in output.

- [ ] **Step 5: Test invalid snapshot**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --status d-fake --in /tmp/vrestic-status-test.yaml`
Expected: Error: `snapshot "d-fake" not found in config`

- [ ] **Step 6: Test with live config**

Run: `cd /home/thorix/git/thorix/vrestic && go run ./ --status all --in config.yaml`
Expected: Table showing status of all snapshots against drobo. This requires Vault access — if running locally, use `VAULT_ADDR` and `VAULT_TOKEN`.
