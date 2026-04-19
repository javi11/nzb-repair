package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestConfig_Par2RecreateThreshold_Default(t *testing.T) {
	cfg := mergeWithDefault()
	assert.Equal(t, 0.0, cfg.Par2RecreateThreshold)
	assert.Equal(t, 10, cfg.Par2RecreateRedundancy)
}

func TestConfig_Par2RecreateThreshold_FromYaml(t *testing.T) {
	yml := `
par2_recreate_threshold: 0.1
par2_recreate_redundancy: 15
`
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte(yml), &cfg))
	cfg = mergeWithDefault(cfg)
	assert.Equal(t, 0.1, cfg.Par2RecreateThreshold)
	assert.Equal(t, 15, cfg.Par2RecreateRedundancy)
}
