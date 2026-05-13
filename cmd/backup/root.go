package backup

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	debug          bool
	dryRun         bool
	quiet          bool
	location       string
	repoBase       string
	listSnapshots  bool
	runAll         bool
	generateName   bool
	unlock         bool
	unlockRemove   bool
	syncConfig     bool
	newSnapshot    string
	runSnapshot    string
	configFile     string
	initSnapshot   string
	statusSnapshot string
	timeout        string
	hostname       string
}

var rootCmd = &cobra.Command{
	Use:   "vrestic",
	Short: "vrestic - Restic backup manager with Vault integration",
	Long: `vrestic manages restic backups using a YAML configuration file.
Passwords are stored in and retrieved from HashiCorp Vault.
Repositories are automatically initialized on first backup.`,
	SilenceUsage: true,
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

		// Status check
		if opts.statusSnapshot != "" {
			locName, loc, err := resolveLocation(cfg, opts.location)
			if err != nil {
				return err
			}
			if opts.repoBase != "" {
				loc.RepoBase = opts.repoBase
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

			for _, name := range names {
				if _, ok := cfg.Snapshots[name]; !ok {
					return fmt.Errorf("snapshot %q not found in config", name)
				}
			}

			var results []restic.StatusResult
			for _, name := range names {
				snap := cfg.Snapshots[name]
				results = append(results, runner.Status(name, snap))
			}

			displayStatus(results, !opts.quiet)
			return nil
		}

		// Resolve location
		locName, loc, err := resolveLocation(cfg, opts.location)
		if err != nil {
			return err
		}
		if opts.repoBase != "" {
			loc.RepoBase = opts.repoBase
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

		// Initialize repo for existing snapshot(s) on a location
		if opts.initSnapshot != "" {
			var names []string
			if opts.initSnapshot == "all" {
				for name := range cfg.Snapshots {
					names = append(names, name)
				}
				sort.Strings(names)
			} else {
				names = splitSnapshots(opts.initSnapshot)
			}
			for _, name := range names {
				if err := runInit(name, cfg, &v, locName, loc); err != nil {
					return err
				}
			}
			return nil
		}

		var timeout time.Duration
		timeoutStr := cfg.Defaults.Timeout
		if opts.timeout != "" {
			timeoutStr = opts.timeout
		}
		if timeoutStr != "" && timeoutStr != "0" {
			var err error
			timeout, err = time.ParseDuration(timeoutStr)
			if err != nil {
				return fmt.Errorf("invalid timeout %q: %w", timeoutStr, err)
			}
		}

		runner := &restic.Runner{
			DryRun:       opts.dryRun,
			Verbose:      !opts.quiet,
			Location:     loc,
			LocationName: locName,
			Vault:        v,
			Defaults:     cfg.Defaults,
			Timeout:      timeout,
			Hostname:     opts.hostname,
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
			var names []string
			if opts.runSnapshot == "all" {
				for name := range cfg.Snapshots {
					names = append(names, name)
				}
				sort.Strings(names)
			} else {
				names = splitSnapshots(opts.runSnapshot)
			}
			// Validate all names exist first (fail-fast)
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
	},
}

func init() {
	rootCmd.Flags().BoolVarP(&opts.debug, "debug", "d", false, "Debug output")
	rootCmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Do not exec commands")
	rootCmd.Flags().BoolVarP(&opts.quiet, "quiet", "q", false, "Suppress progress output")
	rootCmd.Flags().StringVar(&opts.location, "location", "", "Backup location name (defaults to config's defaultLocation)")
	rootCmd.Flags().StringVar(&opts.repoBase, "repo-base", "", "Override location's repo base path (e.g. /mnt/drobo/ for local use)")
	rootCmd.Flags().BoolVarP(&opts.listSnapshots, "list", "l", false, "List all snapshots in the config")
	rootCmd.Flags().BoolVar(&opts.runAll, "all", false, "Run all backups")
	rootCmd.Flags().BoolVarP(&opts.generateName, "generate", "g", false, "Generate a random repo name")
	rootCmd.Flags().StringVarP(&opts.runSnapshot, "backup", "b", "", "Run backup for given snapshot name (comma-separated for multiple)")
	rootCmd.Flags().StringVar(&opts.configFile, "in", "config.yaml", "Configuration file")
	rootCmd.Flags().BoolVar(&opts.unlock, "unlock", false, "Unlock repo(s) instead of running a backup (requires --backup or --all)")
	rootCmd.Flags().BoolVar(&opts.unlockRemove, "unlock-remove-all", false, "When used with --unlock, remove ALL locks including active ones (dangerous)")
	rootCmd.Flags().StringVar(&opts.newSnapshot, "new", "", "Create a new backup snapshot interactively")
	rootCmd.Flags().StringVar(&opts.initSnapshot, "init", "", "Initialize restic repo for an existing snapshot on a location")
	rootCmd.Flags().StringVar(&opts.statusSnapshot, "status", "", "Show backup status for snapshot(s) (name, comma-list, or \"all\")")
	rootCmd.Flags().StringVar(&opts.timeout, "timeout", "", "Override backup timeout (e.g. \"12h\", \"30m\"); use \"0\" to disable")
	rootCmd.Flags().StringVar(&opts.hostname, "hostname", "", "Stable hostname passed to restic --host (enables parent snapshot lookup across pod restarts)")
	rootCmd.Flags().BoolVar(&opts.syncConfig, "sync-config", false, "Pull config from Vault and write to local config file")
}

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
		slog.Info("Repo already exists, skipping", "snapshot", name, "path", repoPath)
		return nil
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
