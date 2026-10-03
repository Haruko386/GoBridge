package tunnel

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

const streamInboundQueueSize = 16

var ErrStreamClosed = errors.New("tunnel stream is closed")

type frameWriter interface {
	WriteFrame(protocol.MessageType, []byte) error
}

type Stream struct {
	id     protocol.StreamID
	writer frameWriter

	onClose func(protocol.StreamID)

	inbound chan []byte

	localDone chan struct{}
	localOnce sync.Once
	closeErr  error

	remoteDone chan struct{}
	remoteOnce sync.Once

	readMu     sync.Mutex
	readBuffer []byte

	writeMu sync.Mutex
}

func newStream(id protocol.StreamID, writer frameWriter, onClose func(protocol.StreamID)) (*Stream, error) {
	if id == 0 {
		return nil, protocol.ErrInvalidStreamID
	}
	if writer == nil {
		return nil, errors.New("stream frame writer is nil")
	}
	if onClose == nil {
		onClose = func(streamID protocol.StreamID) {}
	}

	return &Stream{
		id:         id,
		writer:     writer,
		onClose:    onClose,
		inbound:    make(chan []byte, streamInboundQueueSize),
		localDone:  make(chan struct{}),
		remoteDone: make(chan struct{}),
	}, nil
}

func (s *Stream) ID() protocol.StreamID {
	return s.id
}

func (s *Stream) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}

	s.readMu.Lock()
	defer s.readMu.Unlock()

	for len(s.readBuffer) == 0 {
		data, err := s.nextInbound()
		if err != nil {
			return 0, err
		}

		s.readBuffer = data
	}

	n := copy(buffer, s.readBuffer)
	s.readBuffer = s.readBuffer[n:]

	return n, nil
}

func (s *Stream) nextInbound() ([]byte, error) {
	// A local close aborts reads immediately. In particular, an overloaded
	// stream must not continue draining data that triggered its reset.
	select {
	case <-s.localDone:
		return nil, ErrStreamClosed
	default:
	}

	select {
	case data := <-s.inbound:
		return data, nil
	default:
	}

	select {
	case data := <-s.inbound:
		return data, nil
	case <-s.localDone:
		return nil, ErrStreamClosed
	case <-s.remoteDone:
		select {
		case data := <-s.inbound:
			return data, nil
		default:
			return nil, io.EOF
		}
	}
}

func (s *Stream) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	written := 0

	for written < len(data) {
		if err := s.checkWritable(); err != nil {
			return written, err
		}

		end := written + protocol.MaxStreamDataSize
		if end > len(data) {
			end = len(data)
		}

		payload, err := protocol.EncodeStreamData(s.id, data[written:end])
		if err != nil {
			return written, fmt.Errorf("encode stream %d data: %w", s.id, err)
		}

		if err := s.writer.WriteFrame(protocol.TypeStreamData, payload); err != nil {
			return written, fmt.Errorf("write stream %d data frame: %w", s.id, err)
		}

		written = end
	}

	return written, nil
}

func (s *Stream) checkWritable() error {
	select {
	case <-s.localDone:
		return fmt.Errorf("%w: %d", ErrStreamClosed, s.id)
	default:
	}

	select {
	case <-s.remoteDone:
		return fmt.Errorf("%w: %d", ErrStreamClosed, s.id)
	default:
	}

	return nil
}

func (s *Stream) Close() error {
	s.localOnce.Do(func() {
		close(s.localDone)

		s.closeErr = s.writeCloseFrame()

		s.onClose(s.id)
	})

	return s.closeErr
}

func (s *Stream) isRemoteClosed() bool {
	select {
	case <-s.remoteDone:
		return true
	default:
		return false
	}
}

func (s *Stream) deliver(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf(
			"%w: delivered stream data is empty",
			protocol.ErrMalformedStreamPayload,
		)
	}

	select {
	case <-s.localDone:
		return fmt.Errorf("%w: %d", ErrStreamClosed, s.id)
	default:
	}

	select {
	case <-s.remoteDone:
		return fmt.Errorf("%w: %d", ErrStreamClosed, s.id)
	default:
	}

	select {
	case s.inbound <- data:
		return nil

	case <-s.localDone:
		return fmt.Errorf("%w: %d", ErrStreamClosed, s.id)

	case <-s.remoteDone:
		return fmt.Errorf("%w: %d", ErrStreamClosed, s.id)

	default:
		// The dispatcher owns the only frame-reading goroutine. Waiting for a
		// slow stream here would stall every stream and heartbeat on the
		// session. Reset only this stream and send its close frame separately.
		s.reset()
		return fmt.Errorf("%w: inbound queue full for stream %d", ErrStreamClosed, s.id)
	}
}

func (s *Stream) reset() {
	reset := false

	s.localOnce.Do(func() {
		close(s.localDone)
		s.onClose(s.id)
		reset = true
	})

	if !reset {
		return
	}

	go func() {
		_ = s.writeCloseFrame()
	}()
}

func (s *Stream) writeCloseFrame() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.isRemoteClosed() {
		return nil
	}

	payload, err := protocol.EncodeStreamID(s.id)
	if err != nil {
		return fmt.Errorf("encode CLOSE_STREAM for %d: %w", s.id, err)
	}

	if err := s.writer.WriteFrame(protocol.TypeCloseStream, payload); err != nil {
		return fmt.Errorf("write CLOSE_STREAM for %d: %w", s.id, err)
	}

	return nil
}

func (s *Stream) remoteClose() {
	s.remoteOnce.Do(func() {
		close(s.remoteDone)
	})
}
