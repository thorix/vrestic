package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds the full backup configuration
type Config struct {
	Snapshots map[string]*Snapshot `yaml:"snapshots"`
}

// Snapshot defines a single backup target
type Snapshot struct {
	Repo        string     `yaml:"repo,omitempty"`
	Path        StringList `yaml:"path,omitempty"`
	Exclude     string     `yaml:"exclude,omitempty"`
	CacheDir    string     `yaml:"cacheDir,omitempty"`
	LocalRepo   string     `yaml:"localRepo,omitempty"`
	Retention   string     `yaml:"retention,omitempty"`
	LimitUpload int        `yaml:"limitUpload,omitempty"`
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
