package config

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name string
		role Role
	}{
		{name: "server", role: RoleServer},
		{name: "client", role: RoleClient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := New(tt.role)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if cfg.Version != CurrentVersion {
				t.Errorf("Version = %d, want %d", cfg.Version, CurrentVersion)
			}
			if cfg.Role != tt.role {
				t.Errorf("Role = %q, want %q", cfg.Role, tt.role)
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() error = %v", err)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	server, err := New(RoleServer)
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	if server.Server == nil {
		t.Fatal("server configuration is nil")
	}
	if server.Server.ControlListen != ":18790" {
		t.Errorf("ControlListen = %q, want %q", server.Server.ControlListen, ":18790")
	}
	if server.Server.ProxyListen != ":17897" {
		t.Errorf("ProxyListen = %q, want %q", server.Server.ProxyListen, ":17897")
	}

	client, err := New(RoleClient)
	if err != nil {
		t.Fatalf("New(client) error = %v", err)
	}
	if client.Client == nil {
		t.Fatal("client configuration is nil")
	}
	if client.Client.ServerAddress != "" {
		t.Errorf("ServerAddress = %q, want empty address before pairing", client.Client.ServerAddress)
	}
	if client.Client.ProxyAddress != "127.0.0.1:7897" {
		t.Errorf("ProxyAddress = %q, want %q", client.Client.ProxyAddress, "127.0.0.1:7897")
	}
}

func TestParseRole(t *testing.T) {
	for _, role := range []Role{RoleServer, RoleClient} {
		got, err := ParseRole(string(role))
		if err != nil {
			t.Errorf("ParseRole(%q) error = %v", role, err)
		}
		if got != role {
			t.Errorf("ParseRole(%q) = %q, want %q", role, got, role)
		}
	}

	if _, err := ParseRole("database"); err == nil {
		t.Fatal("ParseRole(database) error = nil, want an error")
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	validServer, err := New(RoleServer)
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	validClient, err := New(RoleClient)
	if err != nil {
		t.Fatalf("New(client) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func() Config
	}{
		{
			name: "unsupported version",
			mutate: func() Config {
				cfg := validServer
				cfg.Version++
				return cfg
			},
		},
		{
			name: "unknown role",
			mutate: func() Config {
				cfg := validServer
				cfg.Role = "database"
				return cfg
			},
		},
		{
			name: "server section missing",
			mutate: func() Config {
				cfg := validServer
				cfg.Server = nil
				return cfg
			},
		},
		{
			name: "server contains client section",
			mutate: func() Config {
				cfg := validServer
				cfg.Client = validClient.Client
				return cfg
			},
		},
		{
			name: "invalid control listen address",
			mutate: func() Config {
				cfg := validServer
				server := *cfg.Server
				server.ControlListen = "invalid"
				cfg.Server = &server
				return cfg
			},
		},
		{
			name: "invalid proxy listen address",
			mutate: func() Config {
				cfg := validServer
				server := *cfg.Server
				server.ProxyListen = "invalid"
				cfg.Server = &server
				return cfg
			},
		},
		{
			name: "client section missing",
			mutate: func() Config {
				cfg := validClient
				cfg.Client = nil
				return cfg
			},
		},
		{
			name: "client contains server section",
			mutate: func() Config {
				cfg := validClient
				cfg.Server = validServer.Server
				return cfg
			},
		},
		{
			name: "invalid client proxy address",
			mutate: func() Config {
				cfg := validClient
				client := *cfg.Client
				client.ProxyAddress = "invalid"
				cfg.Client = &client
				return cfg
			},
		},
		{
			name: "invalid server address",
			mutate: func() Config {
				cfg := validClient
				client := *cfg.Client
				client.ServerAddress = "invalid"
				cfg.Client = &client
				return cfg
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.mutate().Validate(); err == nil {
				t.Fatal("Validate() error = nil, want an error")
			}
		})
	}
}

func TestSaveAndLoad(t *testing.T) {
	for _, role := range []Role{RoleServer, RoleClient} {
		t.Run(string(role), func(t *testing.T) {
			dir := t.TempDir()
			cfg, err := New(role)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			if err := SaveNew(dir, cfg); err != nil {
				t.Fatalf("SaveNew() error = %v", err)
			}

			loaded, err := Load(dir)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if loaded.Role != role {
				t.Errorf("loaded Role = %q, want %q", loaded.Role, role)
			}

			data, err := os.ReadFile(FilePath(dir))
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if role == RoleServer && strings.Contains(string(data), "client:") {
				t.Errorf("server YAML unexpectedly contains client section:\n%s", data)
			}
			if role == RoleClient && strings.Contains(string(data), "server:") {
				t.Errorf("client YAML unexpectedly contains server section:\n%s", data)
			}
		})
	}
}

func TestSaveNewDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	server, err := New(RoleServer)
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	if err := SaveNew(dir, server); err != nil {
		t.Fatalf("first SaveNew() error = %v", err)
	}

	client, err := New(RoleClient)
	if err != nil {
		t.Fatalf("New(client) error = %v", err)
	}
	if err := SaveNew(dir, client); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second SaveNew() error = %v, want ErrAlreadyInitialized", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Role != RoleServer {
		t.Errorf("saved role = %q, want original role %q", loaded.Role, RoleServer)
	}
}

func TestLoadRejectsInvalidData(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(FilePath(dir), []byte("role: ["), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := Load(dir); err == nil {
		t.Fatal("Load() error = nil, want invalid YAML error")
	}
}

func TestSaveNewPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not apply Unix permission bits")
	}

	dir := t.TempDir()
	cfg, err := New(RoleServer)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := SaveNew(dir, cfg); err != nil {
		t.Fatalf("SaveNew() error = %v", err)
	}

	info, err := os.Stat(FilePath(dir))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("config permissions = %o, want 600", got)
	}
}
