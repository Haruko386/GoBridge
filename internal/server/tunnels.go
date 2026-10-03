package server

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/Haruko386/GoBridge/internal/proxy"
)

var (
	ErrProxyProviderOffline   = errors.New("proxy provider is offline")
	ErrMultipleProxyProviders = errors.New("multiple proxy providers are online")
)

type OpenStreamFunc func(context.Context) (proxy.Endpoint, error)

type TunnelRegistry struct {
	mu             sync.RWMutex
	nextGeneration uint64
	tunnels        map[string]tunnelEntry
}

type tunnelEntry struct {
	open       OpenStreamFunc
	generation uint64
}

type TunnelRegistration struct {
	registry   *TunnelRegistry
	nodeID     string
	generation uint64
	once       sync.Once
}

func NewTunnelRegistry() *TunnelRegistry {
	return &TunnelRegistry{tunnels: make(map[string]tunnelEntry)}
}

func (r *TunnelRegistry) Register(nodeID string, open OpenStreamFunc) (*TunnelRegistration, error) {
	if nodeID == "" {
		return nil, errors.New("tunnel node ID is empty")
	}
	if open == nil {
		return nil, errors.New("tunnel open function is nil")
	}

	r.mu.Lock()
	r.nextGeneration++
	generation := r.nextGeneration
	r.tunnels[nodeID] = tunnelEntry{open: open, generation: generation}
	r.mu.Unlock()

	return &TunnelRegistration{registry: r, nodeID: nodeID, generation: generation}, nil
}

func (r *TunnelRegistry) Single() (string, OpenStreamFunc, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.tunnels) == 0 {
		return "", nil, ErrProxyProviderOffline
	}
	if len(r.tunnels) > 1 {
		return "", nil, ErrMultipleProxyProviders
	}

	for nodeID, entry := range r.tunnels {
		return nodeID, entry.open, nil
	}
	panic("unreachable")
}

func (r *TunnelRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tunnels)
}

func (r *TunnelRegistry) NodeIDs() []string {
	r.mu.RLock()
	nodeIDs := make([]string, 0, len(r.tunnels))
	for nodeID := range r.tunnels {
		nodeIDs = append(nodeIDs, nodeID)
	}
	r.mu.RUnlock()

	sort.Strings(nodeIDs)
	return nodeIDs
}

func (r *TunnelRegistration) Remove() bool {
	if r == nil || r.registry == nil {
		return false
	}

	removed := false
	r.once.Do(func() {
		r.registry.mu.Lock()
		defer r.registry.mu.Unlock()

		current, found := r.registry.tunnels[r.nodeID]
		if !found || current.generation != r.generation {
			return
		}

		delete(r.registry.tunnels, r.nodeID)
		removed = true
	})
	return removed
}
