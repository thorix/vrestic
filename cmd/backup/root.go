package backup

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/thorix/vrestic/pkg/config"
	"github.com/thorix/vrestic/pkg/metrics"
	"github.com/thorix/vrestic/pkg/restic"
	"github.com/thorix/vrestic/pkg/shell"
	"github.com/thorix/vrestic/pkg/vault"
)

var opts struct {
	debug         bool
	dryRun        bool
	quiet         bool
	useLocal      bool
	listSnapshots bool
	runAll        bool
	generateName  bool
	unlock        bool
	unlockRemove  bool
	syncConfig    bool
	newSnapshot   string
	runSnapshot   string
	configFile    string
}

var rootCmd = &cobra.Command{
	Use:   "vrestic",
	Short: "vrestic - Restic backup manager with Vault integration",
	Long: `vrestic manages restic backups using a YAML configuration file.
Passwords are stored in and retrieved from HashiCorp Vault.
Repositories are automatically initialized on first backup.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if opts.debug {
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
		}

		// Generate name doesn't need config
		if opts.generateName {
			name, err := restic.PseudoName()
			if err != nil {
				return err
			}
			fmt.Println(name)
			return nil
		}

		// Load config
		slog.Debug("Using configuration file", "file", opts.configFile)
		cfg, err := config.Load(opts.configFile)
		if err != nil && opts.syncConfig {
			// sync-config can work without existing local config
			cfg = &config.Config{Snapshots: make(map[string]*config.Snapshot)}
		} else if err != nil {
			return err
		}

		// List doesn't need Vault
		if opts.listSnapshots {
			displayList(cfg)
			return nil
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
			return runNew(opts.newSnapshot, cfg, &v, opts.configFile)
		}

		var timeout time.Duration
		if cfg.Defaults.Timeout != "" {
			timeout, _ = time.ParseDuration(cfg.Defaults.Timeout)
		}

		runner := &restic.Runner{
			DryRun:   opts.dryRun,
			Verbose:  !opts.quiet,
			UseLocal: opts.useLocal,
			Vault:    v,
			Defaults: cfg.Defaults,
			Timeout:  timeout,
		}
		if cfg.Defaults.MetricsURL != "" {
			runner.Metrics = &metrics.Pusher{URL: cfg.Defaults.MetricsURL}
		}

		if opts.unlock {
			if opts.runSnapshot != "" {
				snap, ok := cfg.Snapshots[opts.runSnapshot]
				if !ok {
					return fmt.Errorf("snapshot %q not found in config", opts.runSnapshot)
				}
				return runner.Unlock(opts.runSnapshot, snap, opts.unlockRemove)
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
			snap, ok := cfg.Snapshots[opts.runSnapshot]
			if !ok {
				return fmt.Errorf("snapshot %q not found in config", opts.runSnapshot)
			}
			return runner.RunTimed(opts.runSnapshot, snap)
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
	},
}

func init() {
	rootCmd.Flags().BoolVarP(&opts.debug, "debug", "d", false, "Debug output")
	rootCmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Do not exec commands")
	rootCmd.Flags().BoolVarP(&opts.quiet, "quiet", "q", false, "Suppress progress output")
	rootCmd.Flags().BoolVar(&opts.useLocal, "local", false, "Use local repo for restic")
	rootCmd.Flags().BoolVarP(&opts.listSnapshots, "list", "l", false, "List all snapshots in the config")
	rootCmd.Flags().BoolVar(&opts.runAll, "all", false, "Run all backups")
	rootCmd.Flags().BoolVarP(&opts.generateName, "generate", "g", false, "Generate a random repo name")
	rootCmd.Flags().StringVarP(&opts.runSnapshot, "backup", "b", "", "Run backup for given snapshot name")
	rootCmd.Flags().StringVar(&opts.configFile, "in", "config.yaml", "Configuration file")
	rootCmd.Flags().BoolVar(&opts.unlock, "unlock", false, "Unlock repo(s) instead of running a backup (requires --backup or --all)")
	rootCmd.Flags().BoolVar(&opts.unlockRemove, "unlock-remove-all", false, "When used with --unlock, remove ALL locks including active ones (dangerous)")
	rootCmd.Flags().StringVar(&opts.newSnapshot, "new", "", "Create a new backup snapshot interactively")
	rootCmd.Flags().BoolVar(&opts.syncConfig, "sync-config", false, "Pull config from Vault and write to local config file")
}

func displayList(cfg *config.Config) {
	keys := make([]string, 0, len(cfg.Snapshots))
	for k := range cfg.Snapshots {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	w := tabwriter.NewWriter(os.Stdout, 10, 8, 2, ' ', 0)
	fmt.Fprintln(w, "SNAPSHOT\tPATH\tREPO")
	for _, name := range keys {
		s := cfg.Snapshots[name]
		repo := s.ResolvedRepo(cfg.Defaults, false)
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, strings.Join(s.Path, ", "), repo)
	}
	w.Flush()
}

func promptLine(reader *bufio.Reader, prompt string, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

func runNew(name string, cfg *config.Config, v *vault.Vault, configFile string) error {
	fmt.Printf("\nCreating new backup snapshot: %s\n\n", name)

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

	// Use defaults from config
	defaultRepoBase := cfg.Defaults.RepoBase
	if defaultRepoBase == "" {
		defaultRepoBase = "/mnt/backup/"
	}
	defaultLocalRepoBase := cfg.Defaults.LocalRepoBase
	defaultRetention := cfg.Defaults.Retention
	if defaultRetention == "" {
		defaultRetention = "1m"
	}

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

	// Prompt for repo base directory (remote/cron path — may not exist locally)
	fmt.Println()
	repoBase := promptLine(reader, "Repo base directory (cron/remote)", defaultRepoBase)
	repoPath := filepath.Join(repoBase, repoName)
	if info, err := os.Stat(repoBase); err != nil {
		fmt.Printf("  ⚠ %s not accessible locally (OK if it's a remote mount)\n", repoBase)
	} else if !info.IsDir() {
		return fmt.Errorf("repo base %q is not a directory", repoBase)
	} else {
		// Check for collision only if path is accessible
		if _, err := os.Stat(repoPath); err == nil {
			return fmt.Errorf("repo path %q already exists (collision)", repoPath)
		}
		fmt.Printf("  ✓ %s accessible, no collision\n", repoBase)
	}
	fmt.Printf("  Generated repo: %s\n", repoPath)

	// Prompt for local repo base (for --local backups, must exist)
	localRepoBase := promptLine(reader, "Local repo base directory (leave empty to use same)", defaultLocalRepoBase)
	var localRepoPath string
	if localRepoBase == "" {
		localRepoPath = repoPath
	} else {
		info, err := os.Stat(localRepoBase)
		if err != nil {
			return fmt.Errorf("local repo base %q: %w", localRepoBase, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("local repo base %q is not a directory", localRepoBase)
		}
		localRepoPath = filepath.Join(localRepoBase, repoName)
		if _, err := os.Stat(localRepoPath); err == nil {
			return fmt.Errorf("local repo path %q already exists (collision)", localRepoPath)
		}
		fmt.Printf("  ✓ Local repo: %s\n", localRepoPath)
	}

	// Prompt for optional settings
	fmt.Println()
	retention := promptLine(reader, "Retention period", defaultRetention)
	defaultLimit := "0"
	if cfg.Defaults.LimitUpload > 0 {
		defaultLimit = strconv.Itoa(cfg.Defaults.LimitUpload)
	}
	limitStr := promptLine(reader, "Upload limit (KiB/s, 0=unlimited)", defaultLimit)
	limitUpload, _ := strconv.Atoi(limitStr)

	// Build snapshot — use repoName when paths match defaults
	snap := &config.Snapshot{
		Path: config.StringList(paths),
	}
	expectedRepo := filepath.Join(defaultRepoBase, repoName)
	expectedLocal := filepath.Join(defaultLocalRepoBase, repoName)
	if defaultLocalRepoBase == "" {
		expectedLocal = expectedRepo
	}
	if repoPath == expectedRepo && localRepoPath == expectedLocal {
		// Both match defaults — just store the name
		snap.RepoName = repoName
	} else if repoPath == expectedRepo && localRepoPath != expectedLocal {
		snap.RepoName = repoName
		snap.LocalRepo = localRepoPath
	} else {
		snap.Repo = repoPath
		snap.LocalRepo = localRepoPath
	}
	if retention != defaultRetention {
		snap.Retention = retention
	}
	if limitUpload > 0 && limitUpload != cfg.Defaults.LimitUpload {
		snap.LimitUpload = limitUpload
	}
	if cfg.Defaults.CacheDir != "" {
		snap.CacheDir = cfg.Defaults.CacheDir
	}

	// Summary
	fmt.Printf("\nSummary:\n")
	fmt.Printf("  Snapshot:  %s\n", name)
	fmt.Printf("  Path:      %s\n", strings.Join(paths, ", "))
	fmt.Printf("  Repo:      %s\n", repoPath)
	if snap.Retention != "" {
		fmt.Printf("  Retention: %s\n", snap.Retention)
	}
	if snap.LimitUpload > 0 {
		fmt.Printf("  Limit:     %d KiB/s\n", snap.LimitUpload)
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

	// Initialize the restic repo (local path if available, otherwise remote)
	initRepo := localRepoPath
	if localRepoPath == repoPath {
		// No separate local path; only init if repo base is accessible
		if _, err := os.Stat(repoBase); err != nil {
			fmt.Printf("\nRepo base not accessible locally — skip init (run first backup on the target host)\n")
			initRepo = ""
		}
	}
	if initRepo != "" {
		fmt.Printf("Initializing restic repo at %s... ", initRepo)
		env := append(os.Environ(), "RESTIC_PASSWORD="+password)
		if err := os.MkdirAll(initRepo, 0755); err != nil {
			return fmt.Errorf("creating repo directory: %w", err)
		}
		initErr := shell.Run(shell.Command{
			Binary: "restic",
			Args:   []string{"-r", initRepo, "init"},
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
	fmt.Printf("  vrestic --backup %s --in %s\n", name, configFile)
	return nil
}

func syncConfig(v *vault.Vault, configFile string) error {
	fmt.Print("Pulling config from Vault (kv/vrestic/config)... ")
	content, err := v.ReadConfig()
	if err != nil {
		return err
	}
	fmt.Println("done")

	fmt.Printf("Writing to %s... ", configFile)
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}
	fmt.Println("done")

	// Load and display what we got
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("verifying synced config: %w", err)
	}
	fmt.Printf("\nSynced %d snapshot(s):\n", len(cfg.Snapshots))
	displayList(cfg)
	return nil
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}
