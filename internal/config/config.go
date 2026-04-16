package config

import (
	"context"
	"os"
	"time"

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

// ProviderConfig holds YAML-friendly NNTP provider settings that map to nntppool/v4 Provider.
type ProviderConfig struct {
	Host        string        `yaml:"host"`
	Username    string        `yaml:"username"`
	Password    string        `yaml:"password"`
	Port        int           `yaml:"port"`
	Connections int           `yaml:"connections"`
	Inflight    int           `yaml:"inflight"`
	TLS         bool          `yaml:"tls"`
	InsecureSSL bool          `yaml:"insecure_ssl"`
	Backup      bool          `yaml:"backup"`
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	SkipPing    bool          `yaml:"skip_ping"`
}

type Config struct {
	// By default the number of connections for download providers is the sum of all Connections
	DownloadWorkers   int              `yaml:"download_workers"`
	UploadWorkers     int              `yaml:"upload_workers"`
	DownloadFolder    string           `yaml:"download_folder"`
	DownloadProviders []ProviderConfig `yaml:"download_providers"`
	UploadProviders   []ProviderConfig `yaml:"upload_providers"`
	Par2Exe           string           `yaml:"par2_exe"`
	Upload            UploadConfig     `yaml:"upload"`
	ScanInterval      time.Duration    `yaml:"scan_interval"` // duration string like "5m", "1h"
	MaxRetries        int64            `yaml:"max_retries"`   // maximum number of retries before moving to broken folder
	BrokenFolder      string           `yaml:"broken_folder"` // folder to move broken files to
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
	providerConfigDefault = ProviderConfig{
		Connections: 10,
		IdleTimeout: 2400 * time.Second,
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
			DownloadProviders: []ProviderConfig{},
			UploadProviders:   []ProviderConfig{},
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
		if p.Connections == 0 {
			p.Connections = providerConfigDefault.Connections
		}

		if p.IdleTimeout == 0 {
			p.IdleTimeout = providerConfigDefault.IdleTimeout
		}

		cfg.DownloadProviders[i] = p
		downloadWorkers += p.Connections
	}

	if cfg.DownloadWorkers == 0 {
		cfg.DownloadWorkers = downloadWorkers
	}

	uploadWorkers := 0
	for i, p := range cfg.UploadProviders {
		if p.Connections == 0 {
			p.Connections = providerConfigDefault.Connections
		}

		if p.IdleTimeout == 0 {
			p.IdleTimeout = providerConfigDefault.IdleTimeout
		}

		cfg.UploadProviders[i] = p
		uploadWorkers += p.Connections
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
