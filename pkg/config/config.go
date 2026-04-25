package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds the full backup configuration
type Config struct {
	Defaults  Defaults             `yaml:"defaults,omitempty"`
	Snapshots map[string]*Snapshot `yaml:"snapshots"`
}

// Defaults holds default values used by --new and backup operations
type Defaults struct {
	DefaultLocation string               `yaml:"defaultLocation,omitempty"`
	Retention       string               `yaml:"retention,omitempty"`
	MetricsURL      string               `yaml:"metricsURL,omitempty"`
	Timeout         string               `yaml:"timeout,omitempty"`
	Locations       map[string]*Location `yaml:"locations,omitempty"`
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

// StringList is a []string that unmarshals from either a single string or a list
type StringList []string

func (s *StringList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		*s = StringList{value.Value}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*s = list
	return nil
}

// Load reads and parses a YAML config file
func Load(configFile string) (*Config, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

// WriteConf marshals the config and writes it to a file
func (c *Config) WriteConf(outputConfigFile string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	err = os.WriteFile(outputConfigFile, data, 0644)
	if err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}
