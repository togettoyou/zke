package server

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	maxHTTPTimeout      = 5 * time.Minute
	maxIdleTimeout      = 10 * time.Minute
	maxDatabaseTimeout  = time.Minute
	maxMigrationTimeout = 10 * time.Minute
	maxShutdownTimeout  = 2 * time.Minute
)

type Config struct {
	HTTP            HTTPConfig
	Database        DatabaseConfig
	ShutdownTimeout time.Duration
	LogLevel        string
}

type HTTPConfig struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type DatabaseConfig struct {
	URL              string
	ConnectTimeout   time.Duration
	MigrationTimeout time.Duration
}

type fileConfig struct {
	HTTP struct {
		Address           string `yaml:"address"`
		ReadHeaderTimeout string `yaml:"read_header_timeout"`
		ReadTimeout       string `yaml:"read_timeout"`
		WriteTimeout      string `yaml:"write_timeout"`
		IdleTimeout       string `yaml:"idle_timeout"`
	} `yaml:"http"`
	Database struct {
		URL              string `yaml:"url"`
		ConnectTimeout   string `yaml:"connect_timeout"`
		MigrationTimeout string `yaml:"migration_timeout"`
	} `yaml:"database"`
	ShutdownTimeout string `yaml:"shutdown_timeout"`
	LogLevel        string `yaml:"log_level"`
}

func LoadConfig(args []string) (Config, error) {
	configPath, err := findConfigPath(args)
	if err != nil {
		return Config{}, err
	}
	if configPath == "" {
		return Config{}, errors.New("--config is required")
	}

	var cfg Config
	if err := applyFile(&cfg, configPath); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func applyFile(cfg *Config, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config file %q: %w", path, err)
	}
	defer file.Close()

	var raw fileConfig
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}
	if err := ensureYAMLEOF(decoder); err != nil {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}

	if raw.HTTP.Address != "" {
		cfg.HTTP.Address = raw.HTTP.Address
	}
	if err := applyDuration(&cfg.HTTP.ReadHeaderTimeout, raw.HTTP.ReadHeaderTimeout, "http.read_header_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.HTTP.ReadTimeout, raw.HTTP.ReadTimeout, "http.read_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.HTTP.WriteTimeout, raw.HTTP.WriteTimeout, "http.write_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.HTTP.IdleTimeout, raw.HTTP.IdleTimeout, "http.idle_timeout"); err != nil {
		return err
	}
	if raw.Database.URL != "" {
		cfg.Database.URL = raw.Database.URL
	}
	if err := applyDuration(&cfg.Database.ConnectTimeout, raw.Database.ConnectTimeout, "database.connect_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Database.MigrationTimeout, raw.Database.MigrationTimeout, "database.migration_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.ShutdownTimeout, raw.ShutdownTimeout, "shutdown_timeout"); err != nil {
		return err
	}
	if raw.LogLevel != "" {
		cfg.LogLevel = raw.LogLevel
	}

	return nil
}

func findConfigPath(args []string) (string, error) {
	fs := flag.NewFlagSet("zke-server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	configPath := ""
	fs.StringVar(&configPath, "config", "", "path to a YAML configuration file")
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("parse flags: %w", err)
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}

	return configPath, nil
}

func (cfg Config) Validate() error {
	if strings.TrimSpace(cfg.HTTP.Address) == "" {
		return errors.New("http address is required")
	}
	if strings.TrimSpace(cfg.Database.URL) == "" {
		return errors.New("PostgreSQL URL is required")
	}
	if strings.TrimSpace(cfg.LogLevel) == "" {
		return errors.New("log level is required")
	}

	for _, item := range []struct {
		value time.Duration
		max   time.Duration
		name  string
	}{
		{cfg.HTTP.ReadHeaderTimeout, maxHTTPTimeout, "http read header timeout"},
		{cfg.HTTP.ReadTimeout, maxHTTPTimeout, "http read timeout"},
		{cfg.HTTP.WriteTimeout, maxHTTPTimeout, "http write timeout"},
		{cfg.HTTP.IdleTimeout, maxIdleTimeout, "http idle timeout"},
		{cfg.Database.ConnectTimeout, maxDatabaseTimeout, "database connect timeout"},
		{cfg.Database.MigrationTimeout, maxMigrationTimeout, "database migration timeout"},
		{cfg.ShutdownTimeout, maxShutdownTimeout, "shutdown timeout"},
	} {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
		if item.value > item.max {
			return fmt.Errorf("%s must not exceed %s", item.name, item.max)
		}
	}

	return nil
}

func applyDuration(target *time.Duration, value, name string) error {
	if value == "" {
		return nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a valid duration: %w", name, err)
	}
	*target = duration
	return nil
}

func ensureYAMLEOF(decoder *yaml.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("configuration contains multiple YAML documents")
}
