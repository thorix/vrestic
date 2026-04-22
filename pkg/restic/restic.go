package restic

import (
	crypto "crypto/rand"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thorix/vrestic/pkg/config"
	"github.com/thorix/vrestic/pkg/metrics"
	"github.com/thorix/vrestic/pkg/shell"
	"github.com/thorix/vrestic/pkg/vault"
)

// Runner orchestrates restic backup operations
type Runner struct {
	DryRun   bool
	Verbose  bool
	UseLocal bool
	Vault    vault.Vault
	Defaults config.Defaults
	Metrics  *metrics.Pusher
	Timeout  time.Duration
	ctx      context.Context // set by RunTimed when timeout is configured
}

// RunTimed wraps Run with timing, timeout, and metrics push.
func (r *Runner) RunTimed(snapshotName string, snap *config.Snapshot) error {
	start := time.Now()

	if r.Timeout > 0 {
		var cancel context.CancelFunc
		r.ctx, cancel = context.WithTimeout(context.Background(), r.Timeout)
		defer func() {
			cancel()
			r.ctx = nil
		}()
	}

	err := r.Run(snapshotName, snap)
	duration := time.Since(start)

	if r.Metrics != nil {
		r.Metrics.Push(metrics.Result{
			Snapshot: snapshotName,
			Success:  err == nil,
			Duration: duration,
			Error:    err,
		})
	}

	if err != nil {
		slog.Error("Backup failed", "snapshot", snapshotName, "duration", duration.Round(time.Second), "error", err)
	} else {
		slog.Info("Backup completed", "snapshot", snapshotName, "duration", duration.Round(time.Second))
	}

	return err
}

// Run performs a full backup for the named snapshot
func (r *Runner) Run(snapshotName string, snap *config.Snapshot) error {
	if len(snap.Path) == 0 {
		return errors.New("snapshot path is missing")
	}

	repoPath := snap.ResolvedRepo(r.Defaults, r.UseLocal)
	if len(repoPath) == 0 {
		return errors.New("snapshot repo is missing (set repoName or repo/localRepo)")
	}

	args := []string{"-r", repoPath, "backup"}
	args = append(args, snap.Path...)
	if r.Verbose {
		args = append(args, "--verbose")
	}
	if len(snap.CacheDir) != 0 {
		args = append(args, "--cache-dir", snap.CacheDir)
	}
	if len(snap.Exclude) != 0 {
		args = append(args, "--exclude", snap.Exclude)
	}
	limitUpload := snap.LimitUpload
	if limitUpload == 0 {
		limitUpload = r.Defaults.LimitUpload
	}
	if limitUpload > 0 && isRemoteRepo(repoPath) {
		args = append(args, "--limit-upload", strconv.Itoa(limitUpload))
	} else if limitUpload > 0 {
		slog.Info("Skipping limitUpload for local repo", "snapshot", snapshotName, "repo", repoPath)
	}

	slog.Info("Running backup", "snapshot", snapshotName)
	if r.DryRun {
		slog.Info("Dry run", "command", fmt.Sprintf("restic %s", strings.Join(args, " ")))
		return nil
	}

	// Check source directories are not empty
	for _, p := range snap.Path {
		slog.Debug("Testing source directory", "path", p)
		empty, err := isDirEmpty(p)
		if err != nil {
			return fmt.Errorf("checking directory %s: %w", p, err)
		}
		if empty {
			return fmt.Errorf("backup source directory is empty: %s (is the folder mounted?)", p)
		}
	}

	passwordValue, err := r.getPassword(snapshotName)
	if err != nil {
		return fmt.Errorf("reading password for %s: %w (use --new to create new snapshots)", snapshotName, err)
	}
	if len(passwordValue) == 0 {
		return fmt.Errorf("password is empty for %s (use --new to create new snapshots)", snapshotName)
	}

	env := os.Environ()
	env = append(env, "RESTIC_PASSWORD="+passwordValue)

	// Verify repo exists (repos are created via --new, not auto-initialized)
	slog.Debug("Verifying restic repo", "path", repoPath)
	err = shell.Run(shell.Command{
		Binary: "restic",
		Args:   []string{"-r", repoPath, "snapshots"},
		Envs:   env,
	})
	if err != nil {
		return fmt.Errorf("repo %s does not exist or is inaccessible (use --new to create new snapshots): %w", repoPath, err)
	}

	// Run the backup
	slog.Debug("Running restic backup", "args", strings.Join(args, " "))
	err = shell.Run(shell.Command{
		Binary: "restic",
		Args:   args,
		Envs:   env,
		Ctx:    r.ctx,
	})
	if err != nil {
		return err
	}

	// Clear any stale locks left behind by prior OOMKilled / interrupted runs
	// before attempting the retention step. `restic unlock` without flags only
	// removes locks older than 30 minutes (so an in-flight backup on another
	// host won't be disturbed).
	slog.Debug("Removing stale locks if any", "repo", repoPath)
	unlockArgs := []string{"-r", repoPath, "unlock"}
	if len(snap.CacheDir) != 0 {
		unlockArgs = append(unlockArgs, "--cache-dir", snap.CacheDir)
	}
	if err := shell.Run(shell.Command{
		Binary: "restic",
		Args:   unlockArgs,
		Envs:   env,
	}); err != nil {
		slog.Warn("restic unlock failed (continuing)", "snapshot", snapshotName, "error", err)
	}

	// Apply retention policy
	retention := snap.Retention
	if retention == "" {
		retention = r.Defaults.Retention
	}
	if retention == "" {
		retention = "1m"
	}
	slog.Info("Applying retention policy", "keep-within", retention)
	forgetArgs := []string{"-r", repoPath, "forget", "--keep-within", retention, "--prune"}
	if r.Verbose {
		forgetArgs = append(forgetArgs, "--verbose")
	}
	if len(snap.CacheDir) != 0 {
		forgetArgs = append(forgetArgs, "--cache-dir", snap.CacheDir)
	}
	return shell.Run(shell.Command{
		Binary: "restic",
		Args:   forgetArgs,
		Envs:   env,
		Ctx:    r.ctx,
	})
}

// Unlock removes stale locks from a snapshot's repo. `removeAll` maps to
// `restic unlock --remove-all`, which also clears non-stale locks — use
// only when you're sure no backup is in progress elsewhere.
func (r *Runner) Unlock(snapshotName string, snap *config.Snapshot, removeAll bool) error {
	repoPath := snap.ResolvedRepo(r.Defaults, r.UseLocal)
	if len(repoPath) == 0 {
		return errors.New("snapshot repo is missing (set repoName or repo/localRepo)")
	}

	if r.DryRun {
		action := "unlock"
		if removeAll {
			action = "unlock --remove-all"
		}
		slog.Info("Dry run", "command", fmt.Sprintf("restic -r %s %s", repoPath, action))
		return nil
	}

	passwordValue, err := r.getPassword(snapshotName)
	if err != nil {
		return err
	}

	env := os.Environ()
	env = append(env, "RESTIC_PASSWORD="+passwordValue)

	args := []string{"-r", repoPath, "unlock"}
	if removeAll {
		args = append(args, "--remove-all")
	}
	if len(snap.CacheDir) != 0 {
		args = append(args, "--cache-dir", snap.CacheDir)
	}
	slog.Info("Unlocking repository", "snapshot", snapshotName, "remove-all", removeAll)
	return shell.Run(shell.Command{
		Binary: "restic",
		Args:   args,
		Envs:   env,
	})
}

// ListSnapshots runs restic snapshots for a given snapshot config
func (r *Runner) ListSnapshots(snapshotName string, snap *config.Snapshot) error {
	repoPath := snap.ResolvedRepo(r.Defaults, r.UseLocal)
	if len(repoPath) == 0 {
		return errors.New("snapshot repo is missing (set repoName or repo/localRepo)")
	}

	if r.DryRun {
		slog.Info("Dry run", "command", fmt.Sprintf("restic -r %s snapshots", repoPath))
		return nil
	}

	passwordValue, err := r.getPassword(snapshotName)
	if err != nil {
		return err
	}

	env := os.Environ()
	env = append(env, "RESTIC_PASSWORD="+passwordValue)

	return shell.Run(shell.Command{
		Binary: "restic",
		Args:   []string{"-r", repoPath, "snapshots"},
		Envs:   env,
	})
}

func (r *Runner) getPassword(name string) (string, error) {
	password, err := r.Vault.ReadPassword(name)
	if err != nil {
		return "", err
	}
	slog.Debug("Using password from Vault", "name", name)
	return password, nil
}

// CreatePassword generates a random password of 60-70 characters
func CreatePassword() string {
	length := 60 + rand.Intn(10)
	var password []string
	startChar := byte('!')
	for i := 0; i < length; i++ {
		newChar := string(startChar + byte(rand.Intn(94)))
		password = append(password, newChar)
	}
	return strings.Join(password, "")
}

// PseudoName generates a random hex name for repo directories
func PseudoName() (string, error) {
	b := make([]byte, 16)
	_, err := crypto.Read(b)
	if err != nil {
		return "", fmt.Errorf("generating name: %w", err)
	}
	return fmt.Sprintf("%X", b[0:8]), nil
}

// isRemoteRepo returns true if the repo path uses a remote backend (sftp, s3, etc.)
func isRemoteRepo(repo string) bool {
	prefixes := []string{"sftp:", "s3:", "rest:", "b2:", "gs:", "azure:", "swift:", "rclone:"}
	for _, p := range prefixes {
		if strings.HasPrefix(repo, p) {
			return true
		}
	}
	return false
}

func isDirEmpty(dirname string) (bool, error) {
	entries, err := os.ReadDir(dirname)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
