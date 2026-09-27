package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	FileName       = "config.yaml"
	CurrentVersion = 1
)

var ErrAlreadyInitialized = errors.New("gobridge is already initialized")

type Role string

const (
	RoleServer Role = "server"
	RoleClient Role = "client"
)

type ServerConfig struct {
	ControlListen string `yaml:"control_listen"`
	ProxyListen   string `yaml:"proxy_listen"`
}

type ClientConfig struct {
	ServerAddress string `yaml:"server_address,omitempty"`
	ProxyAddress  string `yaml:"proxy_address"`
}

type Config struct {
	Version int           `yaml:"version"`
	Role    Role          `yaml:"role"`
	Server  *ServerConfig `yaml:"server,omitempty"`
	Client  *ClientConfig `yaml:"client,omitempty"`
}

func ParseRole(val string) (Role, error) {
	switch Role(val) {
	case RoleServer:
		return RoleServer, nil
	case RoleClient:
		return RoleClient, nil
	default:
		return "", fmt.Errorf("invalid role %q: must be server or client", val)
	}
}

func New(role Role) (Config, error) {
	switch role {
	case RoleServer:
		return Config{
			Version: CurrentVersion,
			Role:    RoleServer,
			Server: &ServerConfig{
				ControlListen: ":18790",
				ProxyListen:   ":17897",
			},
		}, nil
	case RoleClient:
		return Config{
			Version: CurrentVersion,
			Role:    RoleClient,
			Client: &ClientConfig{
				ProxyAddress: "127.0.0.1:7897",
			},
		}, nil
	default:
		return Config{}, fmt.Errorf("unsupported role %q", role)
	}
}

func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}

	switch c.Role {
	case RoleServer:
		if c.Server == nil {
			return errors.New("server configuration is missing")
		}

		if c.Client != nil {
			return errors.New("client configuration should be empty")
		}

		if err := validateAddress("server control listen address", c.Server.ControlListen); err != nil {
			return err
		}
		if err := validateAddress("server proxy listen address", c.Server.ProxyListen); err != nil {
			return err
		}
	case RoleClient:
		if c.Client == nil {
			return errors.New("client configuration is missing")
		}
		if c.Server != nil {
			return errors.New("server configuration should be empty")
		}

		if err := validateAddress("client proxy address", c.Client.ProxyAddress); err != nil {
			return err
		}
		// A client is not paired during initialization, so ServerAddress may be empty.

		if c.Client.ServerAddress != "" {
			if err := validateAddress("client server address", c.Client.ServerAddress); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid role: %s", c.Role)
	}

	return nil
}

func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}

	return filepath.Join(home, ".gobridge"), nil
}

func FilePath(dir string) string {
	return filepath.Join(dir, FileName)
}

func SaveNew(dir string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal configuration: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}

	path := FilePath(dir)

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrAlreadyInitialized, path)
		}

		return fmt.Errorf("create configuration file: %w", err)
	}

	removeIncompleteFile := true
	defer func() {
		if removeIncompleteFile {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()

	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write configuration: %w", err)
	}

	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync configuration: %w", err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("close configuration file: %w", err)
	}

	removeIncompleteFile = false
	return nil
}

func Load(dir string) (Config, error) {
	path := FilePath(dir)

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}

	var cfg Config

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}

	return cfg, nil
}

func validateAddress(name, address string) error {
	if address == "" {
		return fmt.Errorf("%s must not be empty", name)
	}

	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("invalid %s %q: %w", name, address, err)
	}

	return nil
}
