package config

import (
	"crypto/sha256"
	"encoding/hex"
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
	ServerNodeID  string `yaml:"server_node_id,omitempty"`
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
				ProxyListen:   "127.0.0.1:17897",
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

		hasAddress := c.Client.ServerAddress != ""
		hasNodeID := c.Client.ServerNodeID != ""

		if hasAddress != hasNodeID {
			return errors.New("client server address and node ID must either both be set or both be empty")
		}

		if hasAddress {
			if err := validateAddress("client server address", c.Client.ServerAddress); err != nil {
				return err
			}

			if err := validateNodeID(c.Client.ServerNodeID); err != nil {
				return err
			}
		}

		if err := validateAddress("client proxy address", c.Client.ProxyAddress); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid role: %s", c.Role)
	}

	return nil
}

func Save(dir string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}

	path := FilePath(dir)
	if err := writeFileAtomic(path, data); err != nil {
		return fmt.Errorf(
			"save configuration: %w",
			err,
		)
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

	if err := recoverBackup(path); err != nil {
		return Config{}, fmt.Errorf("recover configuration: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}

	var cfg Config

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal configuration: %w", err)
	}

	// Configurations written before ServerNodeID was introduced may contain
	// only ServerAddress. Treat them as unpaired so the pairing command can
	// run again and persist both fields atomically.
	if cfg.Role == RoleClient && cfg.Client != nil &&
		cfg.Client.ServerAddress != "" && cfg.Client.ServerNodeID == "" {
		cfg.Client.ServerAddress = ""
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}

	return cfg, nil
}

func validateNodeID(nodeID string) error {
	decoded, err := hex.DecodeString(nodeID)
	if err != nil {
		return fmt.Errorf("invalid server node ID: %w", err)
	}

	if len(decoded) != sha256.Size {
		return fmt.Errorf("invalid server node ID length: got %d bytes, want %d", len(decoded), sha256.Size)
	}

	return nil
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

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)

	temp, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return err
	}

	tempPath := temp.Name()
	cleanup := true

	defer func() {
		if cleanup {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(0o600); err != nil {
		return err
	}

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}

	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}

	if err := temp.Close(); err != nil {
		return err
	}

	if err := replaceFile(tempPath, path); err != nil {
		return err
	}

	cleanup = false
	return nil
}

func replaceFile(tempPath, targetPath string) error {
	backupPath := targetPath + ".bak"

	if _, err := os.Stat(targetPath); errors.Is(err, os.ErrNotExist) {
		return os.Rename(tempPath, targetPath)
	} else if err != nil {
		return err
	}

	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.Rename(targetPath, backupPath); err != nil {
		return err
	}

	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Rename(backupPath, targetPath)
		return err
	}
	// 新配置已经成功落盘。备份清理失败不能再报告保存失败，
	// 否则内存状态和磁盘状态的判断会不一致。
	_ = os.Remove(backupPath)

	return nil
}

func recoverBackup(path string) error {
	backupPath := path + ".bak"

	_, targetErr := os.Stat(path)
	_, backupErr := os.Stat(backupPath)

	switch {
	case targetErr == nil && backupErr == nil:
		return os.Remove(backupPath)

	case errors.Is(targetErr, os.ErrNotExist) &&
		backupErr == nil:
		return os.Rename(backupPath, path)

	case targetErr != nil &&
		!errors.Is(targetErr, os.ErrNotExist):
		return targetErr

	case backupErr != nil &&
		!errors.Is(backupErr, os.ErrNotExist):
		return backupErr

	default:
		return nil
	}
}
