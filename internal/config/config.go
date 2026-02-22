package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode   string `yaml:"mode"`
	Server struct {
		Host         string   `yaml:"host"`
		Port         int      `yaml:"port"`
		ReadTimeout  string   `yaml:"read_timeout"`
		WriteTimeout string   `yaml:"write_timeout"`
		CORSOrigins  []string `yaml:"cors_origins"`
	} `yaml:"server"`
	Database struct {
		Path           string `yaml:"path"`
		WALMode        bool   `yaml:"wal_mode"`
		MaxConnections int    `yaml:"max_connections"`
		BackupInterval string `yaml:"backup_interval"`
		BackupPath     string `yaml:"backup_path"`
	} `yaml:"database"`
	Auth struct {
		Enabled     bool   `yaml:"enabled"`
		AdminKey    string `yaml:"admin_key"`
		TokenExpiry string `yaml:"token_expiry"`
	} `yaml:"auth"`
	Broker struct {
		ChannelBufferSize int    `yaml:"channel_buffer_size"`
		MessageTTLDefault string `yaml:"message_ttl_default"`
		MaxMessageSizeKB  int    `yaml:"max_message_size_kb"`
	} `yaml:"broker"`
	Knowledge struct {
		MaxEntrySizeKB  int  `yaml:"max_entry_size_kb"`
		FTSEnabled      bool `yaml:"fts_enabled"`
		VersionHistory  bool `yaml:"version_history"`
		MaxVersionsKept int  `yaml:"max_versions_kept"`
	} `yaml:"knowledge"`
	Sync struct {
		Enabled bool     `yaml:"enabled"`
		Remotes []Remote `yaml:"remotes"`
	} `yaml:"sync"`
	UI struct {
		Enabled bool   `yaml:"enabled"`
		Title   string `yaml:"title"`
		Theme   string `yaml:"theme"`
	} `yaml:"ui"`
	Logging struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
		File   string `yaml:"file"`
	} `yaml:"logging"`
}

type Remote struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Key  string `yaml:"api_key"`
	Sync struct {
		Direction        string   `yaml:"direction"`
		Scope            string   `yaml:"scope"`
		CollectionIDs    []string `yaml:"collection_ids"`
		TopicIDs         []string `yaml:"topic_ids"`
		ConflictStrategy string   `yaml:"conflict_strategy"`
		Schedule         string   `yaml:"schedule"`
	} `yaml:"sync"`
}

func Default() Config {
	cfg := Config{
		Mode: "local",
	}
	cfg.Server.Host = "0.0.0.0"
	cfg.Server.Port = 8080
	cfg.Server.ReadTimeout = "30s"
	cfg.Server.WriteTimeout = "30s"
	cfg.Server.CORSOrigins = []string{"http://localhost:3000"}
	cfg.Database.Path = "./opencortex.db"
	cfg.Database.WALMode = true
	cfg.Database.MaxConnections = 10
	cfg.Database.BackupInterval = "1h"
	cfg.Database.BackupPath = "./backups/"
	cfg.Auth.Enabled = true
	cfg.Auth.TokenExpiry = "never"
	cfg.Broker.ChannelBufferSize = 256
	cfg.Broker.MessageTTLDefault = "7d"
	cfg.Broker.MaxMessageSizeKB = 512
	cfg.Knowledge.MaxEntrySizeKB = 1024
	cfg.Knowledge.FTSEnabled = true
	cfg.Knowledge.VersionHistory = true
	cfg.Knowledge.MaxVersionsKept = 100
	cfg.Sync.Enabled = false
	cfg.UI.Enabled = true
	cfg.UI.Title = "Opencortex"
	cfg.UI.Theme = "auto"
	cfg.Logging.Level = "info"
	cfg.Logging.Format = "json"
	return cfg
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}
	overrideFromEnv(&cfg)
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Addr(cfg Config) string {
	return cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.Port)
}

func ReadTimeout(cfg Config) time.Duration {
	d, _ := time.ParseDuration(cfg.Server.ReadTimeout)
	if d == 0 {
		return 30 * time.Second
	}
	return d
}

func WriteTimeout(cfg Config) time.Duration {
	d, _ := time.ParseDuration(cfg.Server.WriteTimeout)
	if d == 0 {
		return 30 * time.Second
	}
	return d
}

func overrideFromEnv(cfg *Config) {
	if v := os.Getenv("OPENCORTEX_MODE"); v != "" {
		cfg.Mode = v
	}
	if v := os.Getenv("OPENCORTEX_SERVER_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("OPENCORTEX_SERVER_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = i
		}
	}
	if v := os.Getenv("OPENCORTEX_DB_PATH"); v != "" {
		cfg.Database.Path = v
	}
	if v := os.Getenv("OPENCORTEX_AUTH_ENABLED"); v != "" {
		cfg.Auth.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("AGENTMESH_ADMIN_KEY"); v != "" {
		cfg.Auth.AdminKey = v
	}
}

func validate(cfg Config) error {
	switch cfg.Mode {
	case "local", "server", "hybrid":
	default:
		return fmt.Errorf("invalid mode: %s", cfg.Mode)
	}
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return errors.New("invalid server.port")
	}
	if cfg.Database.Path == "" {
		return errors.New("database.path is required")
	}
	if cfg.Broker.ChannelBufferSize <= 0 {
		return errors.New("broker.channel_buffer_size must be > 0")
	}
	return nil
}
