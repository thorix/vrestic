package backup

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/thorix/vrestic/pkg/config"
	"github.com/thorix/vrestic/pkg/restic"
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
		if err != nil {
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

		runner := &restic.Runner{
			DryRun:   opts.dryRun,
			Verbose:  !opts.quiet,
			UseLocal: opts.useLocal,
			Vault:    v,
		}

		if opts.runSnapshot != "" {
			snap, ok := cfg.Snapshots[opts.runSnapshot]
			if !ok {
				return fmt.Errorf("snapshot %q not found in config", opts.runSnapshot)
			}
			return runner.Run(opts.runSnapshot, snap)
		}

		if opts.runAll {
			slog.Info("Running all backups")
			var errs []error
			for name, snap := range cfg.Snapshots {
				if err := runner.Run(name, snap); err != nil {
					slog.Error("Backup failed", "snapshot", name, "error", err)
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
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, strings.Join(s.Path, ", "), s.Repo)
	}
	w.Flush()
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}
