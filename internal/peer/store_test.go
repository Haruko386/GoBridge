package peer

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	alice := newTestPeer(t, "alice")
	bob := newTestPeer(t, "bob")
	if err := store.Add(bob); err != nil {
		t.Fatalf("Add(bob) error = %v", err)
	}
	if err := store.Add(alice); err != nil {
		t.Fatalf("Add(alice) error = %v", err)
	}

	got := store.List()
	if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "bob" {
		t.Fatalf("List() = %#v, want alice then bob", got)
	}

	// Returned values must not expose the store's internal public-key slices.
	got[0].PublicKey[0] ^= 0xff
	stored, err := store.Get(alice.NodeID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !bytes.Equal(stored.PublicKey, alice.PublicKey) {
		t.Fatal("mutating List() result changed stored public key")
	}

	key, ok := store.PublicKey(alice.NodeID)
	if !ok {
		t.Fatal("PublicKey() did not find enabled peer")
	}
	key[0] ^= 0xff
	keyAgain, ok := store.PublicKey(alice.NodeID)
	if !ok || !bytes.Equal(keyAgain, alice.PublicKey) {
		t.Fatal("mutating PublicKey() result changed stored public key")
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	reloaded, err := reopened.Get(alice.NodeID)
	if err != nil {
		t.Fatalf("reopened Get() error = %v", err)
	}
	if reloaded.Name != alice.Name || reloaded.Enabled != alice.Enabled ||
		!bytes.Equal(reloaded.PublicKey, alice.PublicKey) {
		t.Fatalf("reloaded peer = %#v, want %#v", reloaded, alice)
	}
}

func TestStoreRejectsDuplicates(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	alice := newTestPeer(t, "alice")
	if err := store.Add(alice); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := store.Add(alice); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Add() error = %v, want ErrAlreadyExists", err)
	}

	sameName := newTestPeer(t, "alice")
	if err := store.Add(sameName); !errors.Is(err, ErrNameAlreadyExists) {
		t.Fatalf("same-name Add() error = %v, want ErrNameAlreadyExists", err)
	}
}

func TestStoreEnableDisableRemovePersistence(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	peer := newTestPeer(t, "alice")
	if err := store.Add(peer); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := store.Disable(peer.NodeID); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if _, ok := store.PublicKey(peer.NodeID); ok {
		t.Fatal("PublicKey() found disabled peer")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() after Disable error = %v", err)
	}
	disabled, err := reopened.Get(peer.NodeID)
	if err != nil || disabled.Enabled {
		t.Fatalf("disabled peer after reopen = %#v, %v", disabled, err)
	}

	if err := reopened.Enable(peer.NodeID); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if err := reopened.Remove(peer.NodeID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := reopened.Get(peer.NodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after Remove error = %v, want ErrNotFound", err)
	}
	if err := reopened.Remove(peer.NodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Remove() error = %v, want ErrNotFound", err)
	}

	finalStore, err := Open(dir)
	if err != nil {
		t.Fatalf("final Open() error = %v", err)
	}
	if got := finalStore.List(); len(got) != 0 {
		t.Fatalf("final List() = %#v, want empty", got)
	}
}

func TestOpenRejectsInvalidFiles(t *testing.T) {
	valid := newTestPeer(t, "alice")
	tests := []struct {
		name string
		data string
	}{
		{"unknown field", "version: 1\nunknown: true\npeers: []\n"},
		{"unsupported version", "version: 2\npeers: []\n"},
		{"invalid public key", "version: 1\npeers:\n  - node_id: " + valid.NodeID + "\n    name: alice\n    public_key: not-base64!\n    enabled: true\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(tt.data), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := Open(dir); err == nil {
				t.Fatal("Open() error = nil, want an error")
			}
		})
	}
}

func TestOpenRecoversBackupWhenStoreIsMissing(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	peer := newTestPeer(t, "alice")
	if err := store.Add(peer); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	path := filepath.Join(dir, FileName)
	backupPath := path + ".bak"
	if err := os.Rename(path, backupPath); err != nil {
		t.Fatalf("Rename(store, backup) error = %v", err)
	}

	recovered, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() during recovery error = %v", err)
	}
	if _, err := recovered.Get(peer.NodeID); err != nil {
		t.Fatalf("Get() after recovery error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("recovered store file Stat() error = %v", err)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still exists after recovery: %v", err)
	}
}

func TestOpenRemovesStaleBackupWhenStoreExists(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	peer := newTestPeer(t, "alice")
	if err := store.Add(peer); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	backupPath := filepath.Join(dir, FileName) + ".bak"
	if err := os.WriteFile(backupPath, []byte("stale backup"), 0o600); err != nil {
		t.Fatalf("WriteFile(backup) error = %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() with stale backup error = %v", err)
	}
	if _, err := reopened.Get(peer.NodeID); err != nil {
		t.Fatalf("Get() after stale-backup cleanup error = %v", err)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale backup still exists: %v", err)
	}
}

func TestMutationsRollbackWhenSaveFails(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	alice := newTestPeer(t, "alice")
	if err := store.Add(alice); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store.path = filepath.Join(blocker, FileName)

	if err := store.Disable(alice.NodeID); err == nil {
		t.Fatal("Disable() error = nil, want save error")
	}
	got, err := store.Get(alice.NodeID)
	if err != nil || !got.Enabled {
		t.Fatalf("peer was not rolled back after Disable: %#v, %v", got, err)
	}

	bob := newTestPeer(t, "bob")
	if err := store.Add(bob); err == nil {
		t.Fatal("Add() error = nil, want save error")
	}
	if _, err := store.Get(bob.NodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("peer was not rolled back after Add; Get() error = %v", err)
	}

	if err := store.Remove(alice.NodeID); err == nil {
		t.Fatal("Remove() error = nil, want save error")
	}
	if _, err := store.Get(alice.NodeID); err != nil {
		t.Fatalf("peer was not rolled back after Remove: %v", err)
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	peer := newTestPeer(t, "alice")
	if err := store.Add(peer); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = store.Get(peer.NodeID)
				_ = store.List()
				_, _ = store.PublicKey(peer.NodeID)
				if (i+j)%2 == 0 {
					_ = store.Enable(peer.NodeID)
				} else {
					_ = store.Disable(peer.NodeID)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestStoreReloadsExternalChanges(t *testing.T) {
	dir := t.TempDir()
	writer, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(writer) error = %v", err)
	}
	reader, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(reader) error = %v", err)
	}

	paired := newTestPeer(t, "external")
	if err := writer.Add(paired); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := reader.Get(paired.NodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() before Reload() error = %v, want ErrNotFound", err)
	}
	if err := reader.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if got, err := reader.Get(paired.NodeID); err != nil || !got.Enabled {
		t.Fatalf("Get() after Reload() = (%#v, %v)", got, err)
	}

	if err := writer.Disable(paired.NodeID); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if err := reader.Reload(); err != nil {
		t.Fatalf("Reload(disabled) error = %v", err)
	}
	if got, err := reader.Get(paired.NodeID); err != nil || got.Enabled {
		t.Fatalf("Get() after disable Reload() = (%#v, %v)", got, err)
	}
}

func newTestPeer(t *testing.T, name string) Peer {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	peer, err := New(name, publicKey)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return peer
}
