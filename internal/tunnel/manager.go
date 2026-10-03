package tunnel

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

const (
	FirstServerStreamID protocol.StreamID = 1
	FirstClientStreamID protocol.StreamID = 2
)

var (
	ErrManagerClosed         = errors.New("tunnel manager is closed")
	ErrStreamIDsExhausted    = errors.New("tunnel stream IDs are exhausted")
	ErrUnexpectedTunnelFrame = errors.New("unexpected tunnel frame")
	ErrStreamOpenRejected    = errors.New("stream open rejected")
)

type IncomingHandler func(stream *Stream)

type OpenRejectedError struct {
	StreamID protocol.StreamID
	Reason   string
}

func (e *OpenRejectedError) Error() string {
	return fmt.Sprintf("open stream %d rejected: %s", e.StreamID, e.Reason)
}

func (e *OpenRejectedError) Unwrap() error {
	return ErrStreamOpenRejected
}

type Manager struct {
	writer   frameWriter
	incoming IncomingHandler

	mu      sync.Mutex
	streams map[protocol.StreamID]*Stream
	pending map[protocol.StreamID]chan error
	nextID  protocol.StreamID
	firstID protocol.StreamID
	closed  bool

	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func NewManager(writer frameWriter, firstID protocol.StreamID, incoming IncomingHandler) (*Manager, error) {
	if writer == nil {
		return nil, fmt.Errorf("tunnel frame writer is nil")
	}

	if firstID != FirstClientStreamID && firstID != FirstServerStreamID {
		return nil, fmt.Errorf("first stream ID must be %d or %d", FirstClientStreamID, FirstServerStreamID)
	}

	return &Manager{
		writer:   writer,
		incoming: incoming,
		streams:  make(map[protocol.StreamID]*Stream),
		pending:  make(map[protocol.StreamID]chan error),
		done:     make(chan struct{}),
		nextID:   firstID,
		firstID:  firstID,
	}, nil
}

func (m *Manager) Open(ctx context.Context) (*Stream, error) {
	if ctx == nil {
		return nil, errors.New("open stream context is nil")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	stream, response, err := m.createLocalStream()
	if err != nil {
		return nil, err
	}

	payload, err := protocol.EncodeStreamID(stream.ID())
	if err != nil {
		m.discardStream(stream)
		return nil, fmt.Errorf("encode OPEN_STREAM for %d: %w", stream.ID(), err)
	}

	if err := m.writer.WriteFrame(
		protocol.TypeOpenStream,
		payload,
	); err != nil {
		m.discardStream(stream)
		return nil, fmt.Errorf("write OPEN_STREAM for %d: %w", stream.ID(), err)
	}

	select {
	case responseErr := <-response:
		if responseErr != nil {
			m.discardStream(stream)
			return nil, responseErr
		}

		if m.isClosed() {
			m.discardStream(stream)
			return nil, ErrManagerClosed
		}

		return stream, nil

	case <-ctx.Done():
		// OPEN_STREAM 可能已经到达对端，所以正常关闭 Stream，
		// 尝试向对端发送 CLOSE_STREAM。
		_ = stream.Close()
		return nil, ctx.Err()

	case <-m.done:
		m.discardStream(stream)
		return nil, ErrManagerClosed
	}
}

func (m *Manager) createLocalStream() (*Stream, chan error, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, nil, ErrManagerClosed
	}

	if m.nextID == 0 {
		return nil, nil, ErrStreamIDsExhausted
	}

	id := m.nextID

	maxID := ^protocol.StreamID(0)
	if id > maxID-2 {
		m.nextID = 0
	} else {
		m.nextID += 2
	}

	stream, err := newStream(id, m.writer, m.removeStream)
	if err != nil {
		return nil, nil, err
	}

	response := make(chan error, 1)

	m.streams[id] = stream
	m.pending[id] = response

	return stream, response, nil
}

func (m *Manager) HandleFrame(frame protocol.Frame) error {
	if m.isClosed() {
		return ErrManagerClosed
	}

	switch frame.Type {
	case protocol.TypeOpenStream:
		return m.handleOpenStream(frame.Payload)

	case protocol.TypeOpenOK:
		return m.handleOpenOK(frame.Payload)

	case protocol.TypeOpenFailed:
		return m.handleOpenFailed(frame.Payload)

	case protocol.TypeStreamData:
		return m.handleStreamData(frame.Payload)

	case protocol.TypeCloseStream:
		return m.handleCloseStream(frame.Payload)

	default:
		return fmt.Errorf("%w: got %s", ErrUnexpectedTunnelFrame, frame.Type)
	}
}

func (m *Manager) handleOpenStream(payload []byte) error {
	id, err := protocol.DecodeStreamID(payload)
	if err != nil {
		return fmt.Errorf("decode OPEN_STREAM: %w", err)
	}

	// 本地使用奇数时，对端必须使用偶数
	if m.isLocalStreamID(id) {
		return m.rejectOpen(
			id,
			"stream ID belongs to the receiving endpoint",
		)
	}

	if m.incoming == nil {
		return m.rejectOpen(id, "incoming streams are not supported")
	}

	stream, err := newStream(
		id,
		m.writer,
		m.removeStream,
	)
	if err != nil {
		return m.rejectOpen(id, err.Error())
	}

	m.mu.Lock()

	if m.closed {
		m.mu.Unlock()
		m.discardStream(stream)
		return ErrManagerClosed
	}

	if _, exists := m.streams[id]; exists {
		m.mu.Unlock()
		return m.rejectOpen(id, "stream ID is already active")
	}

	m.streams[id] = stream
	m.mu.Unlock()

	openOK, err := protocol.EncodeStreamID(id)
	if err != nil {
		m.discardStream(stream)
		return fmt.Errorf("encode OPEN_OK for %d: %w", id, err)
	}

	if err := m.writer.WriteFrame(protocol.TypeOpenOK, openOK); err != nil {
		m.discardStream(stream)
		return fmt.Errorf("write OPEN_OK for %d: %w", id, err)
	}

	if m.isClosed() {
		m.discardStream(stream)
		return ErrManagerClosed
	}

	go m.incoming(stream)

	return nil
}

func (m *Manager) handleOpenOK(payload []byte) error {
	id, err := protocol.DecodeStreamID(payload)
	if err != nil {
		return fmt.Errorf("decode OPEN_OK: %w", err)
	}

	if !m.isLocalStreamID(id) {
		return fmt.Errorf("%w: OPEN_OK stream ID %d belongs to the remote endpoint", ErrUnexpectedTunnelFrame, id)
	}

	response, found := m.takePending(id)
	if !found {
		// Open 可能已经被 Context 取消; 迟到的 OPEN_OK 可以安全忽略。
		return nil
	}

	response <- nil
	return nil
}

func (m *Manager) handleOpenFailed(payload []byte) error {
	id, reason, err := protocol.DecodeOpenFailed(payload)
	if err != nil {
		return fmt.Errorf("decode OPEN_FAILED: %w", err)
	}

	if !m.isLocalStreamID(id) {
		return fmt.Errorf("%w: OPEN_FAILED stream ID %d belongs to the remote endpoint", ErrUnexpectedTunnelFrame, id)
	}

	response, found := m.takePending(id)
	if !found {
		return nil
	}

	response <- &OpenRejectedError{
		StreamID: id,
		Reason:   reason,
	}

	return nil
}

func (m *Manager) handleStreamData(payload []byte) error {
	id, data, err := protocol.DecodeStreamData(payload)
	if err != nil {
		return fmt.Errorf("decode STREAM_DATA: %w", err)
	}

	stream, found := m.getStream(id)
	if !found {
		return nil
	}

	if err := stream.deliver(data); err != nil {
		if errors.Is(err, ErrStreamClosed) {
			return nil
		}

		return fmt.Errorf("deliver data to stream %d: %w", id, err)
	}

	return nil
}

func (m *Manager) handleCloseStream(payload []byte) error {
	id, err := protocol.DecodeStreamID(payload)
	if err != nil {
		return fmt.Errorf("decode CLOSE_STREAM: %w", err)
	}

	stream, found := m.getStream(id)
	if !found {
		return nil
	}
	stream.remoteClose()
	m.removeStream(id)

	return nil
}

func (m *Manager) rejectOpen(
	id protocol.StreamID,
	reason string,
) error {
	payload, err := protocol.EncodeOpenFailed(id, reason)
	if err != nil {
		return fmt.Errorf("encode OPEN_FAILED for %d: %w", id, err)
	}

	if err := m.writer.WriteFrame(
		protocol.TypeOpenFailed,
		payload,
	); err != nil {
		return fmt.Errorf("write OPEN_FAILED for %d: %w", id, err)
	}

	return nil
}

func (m *Manager) getStream(id protocol.StreamID) (*Stream, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stream, found := m.streams[id]
	return stream, found
}

func (m *Manager) takePending(id protocol.StreamID) (chan error, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	response, found := m.pending[id]
	if found {
		delete(m.pending, id)
	}

	return response, found
}

func (m *Manager) removeStream(id protocol.StreamID) {
	m.mu.Lock()
	delete(m.streams, id)
	delete(m.pending, id)
	m.mu.Unlock()
}

func (m *Manager) discardStream(stream *Stream) {
	// 将它标记为远端已关闭，避免 Close 再发送关闭帧
	stream.remoteClose()
	_ = stream.Close()
}

func (m *Manager) isLocalStreamID(
	id protocol.StreamID,
) bool {
	return id%2 == m.firstID%2
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.closed
}

func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.streams)
}

func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()

		m.closed = true
		close(m.done)

		streams := make([]*Stream, 0, len(m.streams))
		for _, stream := range m.streams {
			streams = append(streams, stream)
		}

		clear(m.streams)
		clear(m.pending)

		m.mu.Unlock()

		for _, stream := range streams {
			stream.remoteClose()
			if err := stream.Close(); err != nil {
				m.closeErr = errors.Join(m.closeErr, err)
			}
		}
	})

	return m.closeErr
}
