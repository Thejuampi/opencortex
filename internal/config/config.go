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
	MCP struct {
		Enabled bool `yaml:"enabled"`
		HTTP    struct {
			Enabled bool   `yaml:"enabled"`
			Path    string `yaml:"path"`
		} `yaml:"http"`
		Delivery struct {
			DefaultLeaseSeconds int `yaml:"default_lease_seconds"`
			MaxLeaseSeconds     int `yaml:"max_lease_seconds"`
		} `yaml:"delivery"`
		Tools struct {
			FullParity  bool `yaml:"full_parity"`
			ExposeAdmin bool `yaml:"expose_admin"`
		} `yaml:"tools"`
	} `yaml:"mcp"`
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
	Agents struct {
		AutoDeactivateAfter string `yaml:"auto_deactivate_after"`
	} `yaml:"agents"`
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
	cfg.MCP.Enabled = true
	cfg.MCP.HTTP.Enabled = true
	cfg.MCP.HTTP.Path = "/mcp"
	cfg.MCP.Delivery.DefaultLeaseSeconds = 300
	cfg.MCP.Delivery.MaxLeaseSeconds = 3600
	cfg.MCP.Tools.FullParity = true
	cfg.MCP.Tools.ExposeAdmin = true
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
	cfg.Agents.AutoDeactivateAfter = "168h"
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
	if v := os.Getenv("OPENCORTEX_MCP_ENABLED"); v != "" {
		cfg.MCP.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("OPENCORTEX_MCP_HTTP_ENABLED"); v != "" {
		cfg.MCP.HTTP.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("OPENCORTEX_MCP_HTTP_PATH"); v != "" {
		cfg.MCP.HTTP.Path = v
	}
	if v := os.Getenv("OPENCORTEX_DB_PATH"); v != "" {
		cfg.Database.Path = v
	}
	if v := os.Getenv("OPENCORTEX_AUTH_ENABLED"); v != "" {
		cfg.Auth.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("OPENCORTEX_AGENTS_AUTO_DEACTIVATE_AFTER"); v != "" {
		cfg.Agents.AutoDeactivateAfter = v
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
	if strings.TrimSpace(cfg.MCP.HTTP.Path) == "" || cfg.MCP.HTTP.Path[0] != '/' {
		return errors.New("mcp.http.path must start with '/'")
	}
	if cfg.MCP.Delivery.DefaultLeaseSeconds <= 0 {
		return errors.New("mcp.delivery.default_lease_seconds must be > 0")
	}
	if cfg.MCP.Delivery.MaxLeaseSeconds < cfg.MCP.Delivery.DefaultLeaseSeconds {
		return errors.New("mcp.delivery.max_lease_seconds must be >= mcp.delivery.default_lease_seconds")
	}
	if cfg.Database.Path == "" {
		return errors.New("database.path is required")
	}
	if v := strings.TrimSpace(cfg.Agents.AutoDeactivateAfter); v != "" &&
		v != "0" &&
		!strings.EqualFold(v, "off") &&
		!strings.EqualFold(v, "disabled") {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return errors.New("agents.auto_deactivate_after must be a positive duration or 0/off")
		}
	}
	if cfg.Broker.ChannelBufferSize <= 0 {
		return errors.New("broker.channel_buffer_size must be > 0")
	}
	return nil
}
