package server

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

type Session interface {
	PeerNodeID() string

	WriteFrame(messageType protocol.MessageType, payload []byte) error
	ReadFrame() (protocol.Frame, error)

	LocalAddr() net.Addr
	RemoteAddr() net.Addr

	Close() error
}

type Registry struct {
	mu sync.RWMutex

	nextGeneration uint64
	sessions       map[string]registryEntry
}

type registryEntry struct {
	session    Session
	generation uint64
}

type Registration struct {
	registry   *Registry
	nodeID     string
	generation uint64

	once sync.Once
}

func NewRegistry() *Registry {
	return &Registry{
		sessions: make(map[string]registryEntry),
	}
}

func (r *Registry) Register(session Session) (*Registration, error) {
	if session == nil {
		return nil, errors.New("session is nil")
	}

	nodeID := session.PeerNodeID()
	if nodeID == "" {
		return nil, errors.New("session peer node ID is empty")
	}

	r.mu.Lock()

	r.nextGeneration++
	generation := r.nextGeneration

	previous, replaced := r.sessions[nodeID]

	r.sessions[nodeID] = registryEntry{
		session:    session,
		generation: generation,
	}

	r.mu.Unlock()

	registration := &Registration{
		registry:   r,
		nodeID:     nodeID,
		generation: generation,
	}

	if replaced {
		if err := previous.session.Close(); err != nil {
			return registration, fmt.Errorf("close replaced session for %s: %w", nodeID, err)
		}
	}

	return registration, nil
}

func (r *Registry) Get(nodeID string) (Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, found := r.sessions[nodeID]
	if !found {
		return nil, false
	}

	return entry.session, true
}

func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.sessions)
}

func (r *Registry) NodeIDs() []string {
	r.mu.RLock()

	nodeIDs := make([]string, 0, len(r.sessions))
	for nodeID := range r.sessions {
		nodeIDs = append(nodeIDs, nodeID)
	}

	r.mu.RUnlock()

	sort.Strings(nodeIDs)
	return nodeIDs
}

func (r *Registration) Remove() bool {
	if r == nil || r.registry == nil {
		return false
	}

	removed := false

	r.once.Do(func() {
		registry := r.registry

		registry.mu.Lock()
		defer registry.mu.Unlock()

		current, found := registry.sessions[r.nodeID]
		if !found {
			return
		}

		if current.generation != r.generation {
			return
		}

		delete(r.registry.sessions, r.nodeID)
		removed = true
	})

	return removed
}

func (r *Registry) Close(nodeID string) error {
	r.mu.Lock()

	entry, found := r.sessions[nodeID]
	if found {
		delete(r.sessions, nodeID)
	}

	r.mu.Unlock()

	if !found {
		return nil
	}

	if err := entry.session.Close(); err != nil {
		return fmt.Errorf("close session for %s: %w", nodeID, err)
	}

	return nil
}

func (r *Registry) CloseAll() error {
	r.mu.Lock()

	sessions := make([]Session, 0, len(r.sessions))

	for _, entry := range r.sessions {
		sessions = append(sessions, entry.session)
	}

	clear(r.sessions)
	r.mu.Unlock()

	var result error

	for _, session := range sessions {
		if err := session.Close(); err != nil {
			result = errors.Join(result, err)
		}
	}

	if result != nil {
		return fmt.Errorf("close registered sessions: %w", result)
	}

	return nil
}
