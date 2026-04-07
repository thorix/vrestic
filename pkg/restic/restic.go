package restic

import (
	crypto "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"strconv"
	"strings"

	"github.com/thorix/vrestic/pkg/config"
	"github.com/thorix/vrestic/pkg/shell"
	"github.com/thorix/vrestic/pkg/vault"
)

// Runner orchestrates restic backup operations
type Runner struct {
	DryRun   bool
	Verbose  bool
	UseLocal bool
	Vault    vault.Vault
}

// Run performs a full backup for the named snapshot
func (r *Runner) Run(snapshotName string, snap *config.Snapshot) error {
	if r.UseLocal && len(snap.LocalRepo) == 0 {
		return errors.New("snapshot localRepo is missing")
	}
	if len(snap.Repo) == 0 {
		return errors.New("snapshot repo is missing")
	}
	if len(snap.Path) == 0 {
		return errors.New("snapshot path is missing")
	}

	repoPath := snap.Repo
	if r.UseLocal {
		repoPath = snap.LocalRepo
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
	if snap.LimitUpload > 0 {
		args = append(args, "--limit-upload", strconv.Itoa(snap.LimitUpload))
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
		if strings.HasPrefix(err.Error(), "404") {
			slog.Info("Creating missing password for snapshot", "snapshot", snapshotName)
			err = r.Vault.WritePassword(snapshotName, CreatePassword())
			if err != nil {
				return err
			}
			passwordValue, err = r.getPassword(snapshotName)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	if len(passwordValue) == 0 {
		return errors.New("password missing from Vault")
	}

	env := os.Environ()
	env = append(env, "RESTIC_PASSWORD="+passwordValue)

	// Test if repo exists, init if needed
	slog.Debug("Testing restic repo", "path", repoPath)
	err = shell.Run(shell.Command{
		Binary: "restic",
		Args:   []string{"-r", repoPath, "snapshots"},
		Envs:   env,
	})
	if err != nil {
		slog.Info("Creating repository", "path", repoPath)
		err = shell.Run(shell.Command{
			Binary: "restic",
			Args:   []string{"-r", repoPath, "init"},
			Envs:   env,
		})
		if err != nil {
			return err
		}
	}

	// Run the backup
	slog.Debug("Running restic backup", "args", strings.Join(args, " "))
	err = shell.Run(shell.Command{
		Binary: "restic",
		Args:   args,
		Envs:   env,
	})
	if err != nil {
		return err
	}

	// Apply retention policy
	retention := snap.Retention
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
	})
}

// ListSnapshots runs restic snapshots for a given snapshot config
func (r *Runner) ListSnapshots(snapshotName string, snap *config.Snapshot) error {
	repoPath := snap.Repo
	if r.UseLocal {
		repoPath = snap.LocalRepo
	}
	if len(repoPath) == 0 {
		return errors.New("snapshot repo is missing")
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

func isDirEmpty(dirname string) (bool, error) {
	entries, err := os.ReadDir(dirname)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
