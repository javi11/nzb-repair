package config

import (
	"context"
	"os"
	"time"

	"github.com/javi11/nntppool"
	"gopkg.in/yaml.v3"
)

// Logger interface compatible with slog.Logger
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	DebugContext(ctx context.Context, msg string, args ...any)
	InfoContext(ctx context.Context, msg string, args ...any)
	WarnContext(ctx context.Context, msg string, args ...any)
	ErrorContext(ctx context.Context, msg string, args ...any)
}

type Config struct {
	// By default the number of connections for download providers is the sum of all MaxConnections
	DownloadWorkers   int                             `yaml:"download_workers"`
	UploadWorkers     int                             `yaml:"upload_workers"`
	DownloadFolder    string                          `yaml:"download_folder"`
	DownloadProviders []nntppool.UsenetProviderConfig `yaml:"download_providers"`
	UploadProviders   []nntppool.UsenetProviderConfig `yaml:"upload_providers"`
	Par2Exe           string                          `yaml:"par2_exe"`
	Upload            UploadConfig                    `yaml:"upload"`
	ScanInterval      time.Duration                   `yaml:"scan_interval"` // duration string like "5m", "1h"
	MaxRetries        int64                           `yaml:"max_retries"`   // maximum number of retries before moving to broken folder
	BrokenFolder      string                          `yaml:"broken_folder"` // folder to move broken files to
}

type UploadConfig struct {
	ObfuscationPolicy ObfuscationPolicy `yaml:"obfuscation_policy"`
}

type ObfuscationPolicy string

const (
	ObfuscationPolicyNone ObfuscationPolicy = "none"
	ObfuscationPolicyFull ObfuscationPolicy = "full"
)

type Option func(*Config)

var (
	providerConfigDefault = nntppool.Provider{
		MaxConnections:                 10,
		MaxConnectionIdleTimeInSeconds: 2400,
	}
	downloadWorkersDefault = 10
	uploadWorkersDefault   = 10
	scanIntervalDefault    = 5 * time.Minute
	maxRetriesDefault      = int64(3)
	brokenFolderDefault    = "broken"
)

func mergeWithDefault(config ...Config) Config {
	if len(config) == 0 {
		return Config{
			DownloadProviders: []nntppool.UsenetProviderConfig{},
			UploadProviders:   []nntppool.UsenetProviderConfig{},
			DownloadWorkers:   downloadWorkersDefault,
			UploadWorkers:     uploadWorkersDefault,
			DownloadFolder:    "./",
			ScanInterval:      scanIntervalDefault,
			MaxRetries:        maxRetriesDefault,
			BrokenFolder:      brokenFolderDefault,
		}
	}

	cfg := config[0]

	downloadWorkers := 0
	for i, p := range cfg.DownloadProviders {
		if p.MaxConnections == 0 {
			p.MaxConnections = providerConfigDefault.MaxConnections
		}

		if p.MaxConnectionIdleTimeInSeconds == 0 {
			p.MaxConnectionIdleTimeInSeconds = providerConfigDefault.MaxConnectionIdleTimeInSeconds
		}

		cfg.DownloadProviders[i] = p
		downloadWorkers += p.MaxConnections
	}

	if cfg.DownloadWorkers == 0 {
		cfg.DownloadWorkers = downloadWorkers
	}

	uploadWorkers := 0
	for i, p := range cfg.UploadProviders {
		if p.MaxConnections == 0 {
			p.MaxConnections = providerConfigDefault.MaxConnections
		}

		if p.MaxConnectionIdleTimeInSeconds == 0 {
			p.MaxConnectionIdleTimeInSeconds = providerConfigDefault.MaxConnectionIdleTimeInSeconds
		}

		cfg.UploadProviders[i] = p
		uploadWorkers += p.MaxConnections
	}

	if cfg.UploadWorkers == 0 {
		cfg.UploadWorkers = uploadWorkers
	}

	if cfg.ScanInterval == 0 {
		cfg.ScanInterval = scanIntervalDefault
	}

	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = maxRetriesDefault
	}

	if cfg.BrokenFolder == "" {
		cfg.BrokenFolder = brokenFolderDefault
	}

	return cfg
}

func NewFromFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}

	return mergeWithDefault(cfg), nil
}
