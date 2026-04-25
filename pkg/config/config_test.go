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
