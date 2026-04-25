package restic

import (
	"os"
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
	require.NoError(t, os.WriteFile(dir+"/testfile", []byte("test"), 0644))
	result := checkSource([]string{dir})
	assert.Equal(t, "ok", result)
}

func TestCheckSource_MixedPaths(t *testing.T) {
	dir := t.TempDir()
	result := checkSource([]string{dir, "/nonexistent/path"})
	assert.Equal(t, "NOT MOUNTED", result)
}
