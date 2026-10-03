package peer

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	FileName          = "peers.yaml"
	CurrentVersion    = 1
	replaceAttempts   = 20
	replaceRetryDelay = 5 * time.Millisecond
)

var (
	ErrAlreadyExists     = errors.New("peer already exists")
	ErrNameAlreadyExists = errors.New("peer name already exists")
	ErrNotFound          = errors.New("peer not found")
)

type Store struct {
	mu    sync.RWMutex
	path  string
	peers map[string]Peer
}

type diskPeer struct {
	NodeID    string `yaml:"node_id"`
	Name      string `yaml:"name"`
	PublicKey string `yaml:"public_key"`
	Enabled   bool   `yaml:"enabled"`
}

type diskStore struct {
	Version int        `yaml:"version"`
	Peers   []diskPeer `yaml:"peers"`
}

func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("dir is empty")
	}

	store := &Store{
		path:  filepath.Join(dir, FileName),
		peers: make(map[string]Peer),
	}

	if err := recoverBackup(store.path); err != nil {
		return nil, fmt.Errorf("recover peer store: %w", err)
	}

	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("read peer store: %w", err)
	}

	loaded, err := decodePeers(data)
	if err != nil {
		return nil, err
	}
	store.peers = loaded

	return store, nil
}

func (s *Store) Reload() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read peer store: %w", err)
	}

	loaded, err := decodePeers(data)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.peers = loaded
	s.mu.Unlock()
	return nil
}

func decodePeers(data []byte) (map[string]Peer, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var disk diskStore
	if err := decoder.Decode(&disk); err != nil {
		return nil, fmt.Errorf("decode peer store: %w", err)
	}
	if disk.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported peer store version: got %d, want %d", disk.Version, CurrentVersion)
	}

	peers := make(map[string]Peer, len(disk.Peers))
	names := make(map[string]struct{}, len(disk.Peers))
	for _, value := range disk.Peers {
		publicKey, err := base64.StdEncoding.DecodeString(value.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("decode peer public key: %w", err)
		}

		current := Peer{NodeID: value.NodeID, Name: value.Name, PublicKey: ed25519.PublicKey(publicKey), Enabled: value.Enabled}
		if err := current.Validate(); err != nil {
			return nil, fmt.Errorf("validate peer %q: %w", value.NodeID, err)
		}
		if _, exists := peers[current.NodeID]; exists {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, current.NodeID)
		}
		if _, exists := names[current.Name]; exists {
			return nil, fmt.Errorf("%w: %s", ErrNameAlreadyExists, current.Name)
		}

		names[current.Name] = struct{}{}
		peers[current.NodeID] = clonePeer(current)
	}
	return peers, nil
}

func (s *Store) Add(peer Peer) error {
	if err := peer.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.peers[peer.NodeID]; exists {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, peer.NodeID)
	}

	for _, existingPeer := range s.peers {
		if existingPeer.Name == peer.Name {
			return fmt.Errorf("%w: %s", ErrNameAlreadyExists, peer.Name)
		}
	}

	s.peers[peer.NodeID] = clonePeer(peer)

	if err := s.saveLocked(); err != nil {
		delete(s.peers, peer.NodeID)
		return err
	}
	return nil
}

func (s *Store) Get(nodeID string) (Peer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	found, ok := s.peers[nodeID]
	if !ok {
		return Peer{}, fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}

	return clonePeer(found), nil
}

func (s *Store) List() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peers := make([]Peer, 0, len(s.peers))

	for _, peer := range s.peers {
		peers = append(peers, clonePeer(peer))
	}

	sort.Slice(peers, func(i, j int) bool {
		if peers[i].Name == peers[j].Name {
			return peers[i].NodeID < peers[j].NodeID
		}
		return peers[i].Name < peers[j].Name
	})

	return peers
}

func (s *Store) Enable(nodeID string) error {
	return s.setEnabled(nodeID, true)
}

func (s *Store) Disable(nodeID string) error {
	return s.setEnabled(nodeID, false)
}

func (s *Store) setEnabled(nodeID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.peers[nodeID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}

	if current.Enabled == enabled {
		return nil
	}

	previous := current

	current.Enabled = enabled
	s.peers[nodeID] = current

	if err := s.saveLocked(); err != nil {
		s.peers[nodeID] = previous
		return err
	}

	return nil
}

func (s *Store) Remove(nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous, ok := s.peers[nodeID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}

	delete(s.peers, nodeID)

	if err := s.saveLocked(); err != nil {
		s.peers[nodeID] = previous
		return err
	}
	return nil
}

func (s *Store) PublicKey(nodeID string) (ed25519.PublicKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	found, ok := s.peers[nodeID]
	if !ok || !found.Enabled {
		return nil, false
	}

	return bytes.Clone(found.PublicKey), true
}

func (s *Store) saveLocked() error {
	peers := make([]Peer, 0, len(s.peers))

	for _, current := range s.peers {
		peers = append(peers, current)
	}

	sort.Slice(peers, func(i, j int) bool {
		if peers[i].Name == peers[j].Name {
			return peers[i].NodeID < peers[j].NodeID
		}
		return peers[i].Name < peers[j].Name
	})

	disk := diskStore{
		Version: CurrentVersion,
		Peers:   make([]diskPeer, 0, len(peers)),
	}

	for _, current := range peers {
		disk.Peers = append(disk.Peers, diskPeer{
			NodeID:    current.NodeID,
			Name:      current.Name,
			PublicKey: base64.StdEncoding.EncodeToString(current.PublicKey),
			Enabled:   current.Enabled,
		})
	}

	data, err := yaml.Marshal(disk)
	if err != nil {
		return fmt.Errorf("marshal peer store: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("mkdir peer store: %w", err)
	}

	if err := writeFileAtomic(s.path, data); err != nil {
		return fmt.Errorf("write peer store: %w", err)
	}

	return nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)

	temp, err := os.CreateTemp(dir, ".peers-*.tmp")
	if err != nil {
		return err
	}

	tempPath := temp.Name()
	cleanup := true

	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
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
		return renameWithRetry(tempPath, targetPath)
	} else if err != nil {
		return err
	}

	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := renameWithRetry(targetPath, backupPath); err != nil {
		return err
	}

	if err := renameWithRetry(tempPath, targetPath); err != nil {
		_ = renameWithRetry(backupPath, targetPath)
		return err
	}

	_ = os.Remove(backupPath)
	return nil
}

func renameWithRetry(oldPath, newPath string) error {
	var err error
	for attempt := 0; attempt < replaceAttempts; attempt++ {
		if err = os.Rename(oldPath, newPath); err == nil {
			return nil
		}
		if attempt+1 < replaceAttempts {
			time.Sleep(replaceRetryDelay)
		}
	}
	return err
}

func recoverBackup(path string) error {
	backupPath := path + ".bak"

	_, targetErr := os.Stat(path)
	_, backupErr := os.Stat(backupPath)

	switch {
	case targetErr == nil && backupErr == nil:
		// 新文件已经安装成功，只剩尚未清理的旧备份。
		return os.Remove(backupPath)

	case errors.Is(targetErr, os.ErrNotExist) && backupErr == nil:
		// 程序在旧文件改名后退出，恢复旧文件。
		return os.Rename(backupPath, path)

	case targetErr != nil && !errors.Is(targetErr, os.ErrNotExist):
		return targetErr

	case backupErr != nil && !errors.Is(backupErr, os.ErrNotExist):
		return backupErr

	default:
		return nil
	}
}
