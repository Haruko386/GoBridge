package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Haruko386/GoBridge/internal/peer"
)

type PeerWatcher struct {
	store       *peer.Store
	registry    *Registry
	interval    time.Duration
	reportError ErrorHandler
}

func NewPeerWatcher(store *peer.Store, registry *Registry, interval time.Duration, reportError ErrorHandler) (*PeerWatcher, error) {
	if store == nil {
		return nil, errors.New("peer watcher store is nil")
	}
	if registry == nil {
		return nil, errors.New("peer watcher registry is nil")
	}
	if interval <= 0 {
		return nil, errors.New("peer watcher interval must be positive")
	}
	if reportError == nil {
		reportError = func(error) {}
	}
	return &PeerWatcher{store: store, registry: registry, interval: interval, reportError: reportError}, nil
}

func (w *PeerWatcher) Serve(ctx context.Context) error {
	if ctx == nil {
		return errors.New("peer watcher context is nil")
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.reconcile()
		}
	}
}

func (w *PeerWatcher) reconcile() {
	if err := w.store.Reload(); err != nil {
		w.reportError(fmt.Errorf("reload peer store: %w", err))
		return
	}

	for _, nodeID := range w.registry.NodeIDs() {
		paired, err := w.store.Get(nodeID)
		if err == nil && paired.Enabled {
			continue
		}
		if err != nil && !errors.Is(err, peer.ErrNotFound) {
			w.reportError(fmt.Errorf("look up connected peer %s: %w", nodeID, err))
			continue
		}
		if err := w.registry.Close(nodeID); err != nil {
			w.reportError(fmt.Errorf("close unauthorized peer %s: %w", nodeID, err))
		}
	}
}
