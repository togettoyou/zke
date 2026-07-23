package agent

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ServerAddress string
	LogLevel      string
}

type fileConfig struct {
	ServerAddress string `yaml:"server_address"`
	LogLevel      string `yaml:"log_level"`
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
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode config file %q: multiple YAML documents", path)
		}
		return fmt.Errorf("decode config file %q: %w", path, err)
	}

	if raw.ServerAddress != "" {
		cfg.ServerAddress = raw.ServerAddress
	}
	if raw.LogLevel != "" {
		cfg.LogLevel = raw.LogLevel
	}

	return nil
}

func findConfigPath(args []string) (string, error) {
	fs := flag.NewFlagSet("zke-agent", flag.ContinueOnError)
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
	serverURL, err := url.Parse(cfg.ServerAddress)
	if err != nil {
		return errors.New("server address must be a valid URL")
	}
	if serverURL.Scheme != "https" || serverURL.Host == "" {
		return errors.New("server address must use HTTPS and include a host")
	}
	if serverURL.User != nil {
		return errors.New("server address must not contain credentials")
	}
	if strings.TrimSpace(cfg.LogLevel) == "" {
		return errors.New("log level is required")
	}
	return nil
}

func (cfg Config) ServerHost() string {
	serverURL, err := url.Parse(cfg.ServerAddress)
	if err != nil {
		return ""
	}
	return serverURL.Host
}
