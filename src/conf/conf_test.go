package conf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGet_ReturnsConfig(t *testing.T) {
	c := Get()
	assert.NotNil(t, c)
	assert.Equal(t, c, conf)
}

func TestLoad_Defaults(t *testing.T) {
	// Reset conf to defaults
	old := *conf
	defer func() { *conf = old }()

	confFile = ""
	conf.Global.DataPath = ""
	conf.Global.Port = 0
	conf.Server.CacheSize = 0
	conf.Server.IndexWorkers = 0
	conf.Server.MaxFileSize = 0

	tmpDir := t.TempDir()

	// Point config search to temp dir so no config file is found
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := Load()
	assert.NoError(t, err)

	// Verify defaults were applied
	assert.NotEmpty(t, conf.Global.DataPath)
	assert.True(t, conf.Server.MaxFileSize > 0)
	assert.True(t, conf.Server.CacheSize > 0)
	assert.True(t, conf.Server.IndexWorkers > 0)
}

func TestLoad_WithConfigFile(t *testing.T) {
	old := *conf
	defer func() { *conf = old }()

	confFile = ""
	tmpDir := t.TempDir()

	configContent := `
global:
  port: 12345
server:
  cache_size: 32
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	os.WriteFile(configPath, []byte(configContent), 0644)

	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	confFile = configPath
	err := Load()
	assert.NoError(t, err)
	assert.Equal(t, 12345, conf.Global.Port)
}

func TestLoad_PortValidation(t *testing.T) {
	old := *conf
	defer func() { *conf = old }()

	confFile = ""

	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Port out of range should be reset to default
	conf.Global.Port = 99999
	conf.Global.SocketPath = ""
	Load()
	assert.Equal(t, DefaultPort, conf.Global.Port)
}

func TestLoad_SearchLimitsValidation(t *testing.T) {
	old := *conf
	defer func() { *conf = old }()

	confFile = ""

	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Invalid limits should be reset
	conf.Server.Search.Limit.MaxResults = -1
	conf.Server.Search.Limit.MaxResultsPerFile = -1
	conf.Server.Search.MaxWildcardLength = -1
	conf.Server.Search.MaxKeywordDistance = -1
	Load()
	assert.True(t, conf.Server.Search.Limit.MaxResults > 0)
	assert.True(t, conf.Server.Search.Limit.MaxResultsPerFile > 0)
	assert.True(t, conf.Server.Search.MaxWildcardLength > 0)
	assert.True(t, conf.Server.Search.MaxKeywordDistance > 0)
}

func TestDefaultValues(t *testing.T) {
	assert.Equal(t, int64(5*1024*1024), int64(DefaultMaxFileSize))
	assert.Equal(t, 6, DefaultIndexWorkers)
	assert.Equal(t, 13134, DefaultPort)
	assert.Equal(t, 24, DefaultMaxSearchWildcardLength)
	assert.Equal(t, 32, DefaultMaxSearchKeywordDistance)
}
