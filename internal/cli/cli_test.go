package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantExitCode   int
		wantStdoutText string
		wantStderrText string
	}{
		{
			name:           "no arguments shows help",
			wantStdoutText: "Usage:",
		},
		{
			name:           "help command shows help",
			args:           []string{"help"},
			wantStdoutText: "Usage:",
		},
		{
			name:           "help flag shows help",
			args:           []string{"--help"},
			wantStdoutText: "Usage:",
		},
		{
			name:           "version command shows version",
			args:           []string{"version"},
			wantStdoutText: "gobridge test-version",
		},
		{
			name:           "short version flag shows version",
			args:           []string{"-v"},
			wantStdoutText: "gobridge test-version",
		},
		{
			name:           "long version flag shows version",
			args:           []string{"--version"},
			wantStdoutText: "gobridge test-version",
		},
		{
			name:           "unknown command reports error",
			args:           []string{"unknown"},
			wantExitCode:   2,
			wantStderrText: `unknown command "unknown"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			exitCode := Run(tt.args, &stdout, &stderr, "test-version")

			if exitCode != tt.wantExitCode {
				t.Errorf("Run() exit code = %d, want %d", exitCode, tt.wantExitCode)
			}
			assertOutput(t, "stdout", stdout.String(), tt.wantStdoutText)
			assertOutput(t, "stderr", stderr.String(), tt.wantStderrText)
		})
	}
}

func TestRunInit(t *testing.T) {
	tests := []struct {
		name string
		role config.Role
	}{
		{name: "server", role: config.RoleServer},
		{name: "client", role: config.RoleClient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			exitCode := Run(
				[]string{"init", "--role", string(tt.role), "--config-dir", dir},
				&stdout,
				&stderr,
				"test-version",
			)

			if exitCode != 0 {
				t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Errorf("Run() stderr = %q, want empty output", stderr.String())
			}
			if !strings.Contains(stdout.String(), "Initialized GoBridge "+string(tt.role)) {
				t.Errorf("Run() stdout = %q, want initialization message", stdout.String())
			}

			nodeIdentity, err := identity.Load(dir)
			if err != nil {
				t.Fatalf("identity.Load() error = %v", err)
			}

			if !strings.Contains(
				stdout.String(),
				nodeIdentity.NodeID(),
			) {
				t.Errorf(
					"Run() stdout = %q, want node ID %q",
					stdout.String(),
					nodeIdentity.NodeID(),
				)
			}

			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatalf("config.Load() error = %v", err)
			}
			if cfg.Role != tt.role {
				t.Errorf("config role = %q, want %q", cfg.Role, tt.role)
			}
		})
	}
}

func TestRunInitUsageErrors(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantOutput string
	}{
		{
			name:       "missing role",
			args:       []string{"init", "--config-dir", t.TempDir()},
			wantOutput: "--role is required",
		},
		{
			name:       "invalid role",
			args:       []string{"init", "--role", "database", "--config-dir", t.TempDir()},
			wantOutput: `invalid role "database"`,
		},
		{
			name:       "unexpected positional argument",
			args:       []string{"init", "extra", "--role", "server"},
			wantOutput: "unexpected arguments",
		},
		{
			name:       "unknown flag",
			args:       []string{"init", "--unknown"},
			wantOutput: "flag provided but not defined",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			exitCode := Run(tt.args, &stdout, &stderr, "test-version")

			if exitCode != 2 {
				t.Errorf("Run() exit code = %d, want 2", exitCode)
			}
			if stdout.Len() != 0 {
				t.Errorf("Run() stdout = %q, want empty output", stdout.String())
			}
			if !strings.Contains(stderr.String(), tt.wantOutput) {
				t.Errorf("Run() stderr = %q, want it to contain %q", stderr.String(), tt.wantOutput)
			}
		})
	}
}

func TestRunInitHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run([]string{"init", "--help"}, &stdout, &stderr, "test-version")

	if exitCode != 0 {
		t.Errorf("Run() exit code = %d, want 0", exitCode)
	}
	if !strings.Contains(stderr.String(), "Usage: gobridge init") {
		t.Errorf("Run() stderr = %q, want init usage", stderr.String())
	}
}

func TestRunInitDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	args := []string{"init", "--role", "server", "--config-dir", dir}

	if exitCode := Run(args, &bytes.Buffer{}, &bytes.Buffer{}, "test-version"); exitCode != 0 {
		t.Fatalf("first Run() exit code = %d, want 0", exitCode)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(args, &stdout, &stderr, "test-version")

	if exitCode != 1 {
		t.Errorf("second Run() exit code = %d, want 1", exitCode)
	}
	if stdout.Len() != 0 {
		t.Errorf("second Run() stdout = %q, want empty output", stdout.String())
	}
	if !strings.Contains(stderr.String(), config.ErrAlreadyInitialized.Error()) {
		t.Errorf("second Run() stderr = %q, want already-initialized error", stderr.String())
	}
}

func assertOutput(t *testing.T, stream, got, wantSubstring string) {
	t.Helper()

	if wantSubstring == "" {
		if got != "" {
			t.Errorf("%s = %q, want empty output", stream, got)
		}
		return
	}

	if !strings.Contains(got, wantSubstring) {
		t.Errorf("%s = %q, want it to contain %q", stream, got, wantSubstring)
	}
}

func TestRunInitRollsBackConfigWhenIdentityExists(
	t *testing.T,
) {
	dir := t.TempDir()

	existingIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}

	if err := identity.SaveNew(
		dir,
		existingIdentity,
	); err != nil {
		t.Fatalf("identity.SaveNew() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run(
		[]string{
			"init",
			"--role", "server",
			"--config-dir", dir,
		},
		&stdout,
		&stderr,
		"test-version",
	)

	if exitCode != 1 {
		t.Errorf(
			"Run() exit code = %d, want 1",
			exitCode,
		)
	}

	if _, err := os.Stat(config.FilePath(dir)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Errorf(
			"config file still exists after rollback: %v",
			err,
		)
	}

	loadedIdentity, err := identity.Load(dir)
	if err != nil {
		t.Fatalf(
			"identity.Load() after failed init: %v",
			err,
		)
	}

	if loadedIdentity.NodeID() != existingIdentity.NodeID() {
		t.Fatal("existing identity was modified")
	}
}
