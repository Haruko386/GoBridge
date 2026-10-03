package tunnel

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

type recordedFrame struct {
	typ     protocol.MessageType
	payload []byte
}

type recordingWriter struct {
	mu       sync.Mutex
	calls    int
	frames   []recordedFrame
	failCall int
	failErr  error
	started  chan struct{}
	release  chan struct{}
	startOne sync.Once
}

func (w *recordingWriter) WriteFrame(typ protocol.MessageType, payload []byte) error {
	w.mu.Lock()
	w.calls++
	call := w.calls
	w.mu.Unlock()

	if call == 1 && w.started != nil {
		w.startOne.Do(func() { close(w.started) })
		<-w.release
	}

	if call == w.failCall {
		return w.failErr
	}

	w.mu.Lock()
	w.frames = append(w.frames, recordedFrame{
		typ:     typ,
		payload: bytes.Clone(payload),
	})
	w.mu.Unlock()
	return nil
}

func (w *recordingWriter) snapshot() []recordedFrame {
	w.mu.Lock()
	defer w.mu.Unlock()

	frames := make([]recordedFrame, len(w.frames))
	copy(frames, w.frames)
	return frames
}

func TestNewStreamValidatesArguments(t *testing.T) {
	writer := &recordingWriter{}

	if _, err := newStream(0, writer, nil); !errors.Is(err, protocol.ErrInvalidStreamID) {
		t.Fatalf("newStream(0) error = %v, want ErrInvalidStreamID", err)
	}

	if _, err := newStream(1, nil, nil); err == nil {
		t.Fatal("newStream(nil writer) error = nil, want an error")
	}

	stream, err := newStream(7, writer, nil)
	if err != nil {
		t.Fatalf("newStream() error = %v", err)
	}
	if got := stream.ID(); got != 7 {
		t.Fatalf("ID() = %d, want 7", got)
	}
}

func TestStreamWriteSplitsAndEncodesData(t *testing.T) {
	writer := &recordingWriter{}
	stream := mustNewStream(t, 9, writer, nil)
	data := bytes.Repeat([]byte("g"), 2*protocol.MaxStreamDataSize+7)

	written, err := stream.Write(data)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if written != len(data) {
		t.Fatalf("Write() wrote %d bytes, want %d", written, len(data))
	}

	frames := writer.snapshot()
	if len(frames) != 3 {
		t.Fatalf("frame count = %d, want 3", len(frames))
	}

	var decoded bytes.Buffer
	for index, frame := range frames {
		if frame.typ != protocol.TypeStreamData {
			t.Fatalf("frame %d type = %v, want STREAM_DATA", index, frame.typ)
		}

		id, chunk, err := protocol.DecodeStreamData(frame.payload)
		if err != nil {
			t.Fatalf("DecodeStreamData(frame %d) error = %v", index, err)
		}
		if id != stream.ID() {
			t.Fatalf("frame %d stream ID = %d, want %d", index, id, stream.ID())
		}
		decoded.Write(chunk)
	}

	if !bytes.Equal(decoded.Bytes(), data) {
		t.Fatal("decoded frame data does not equal input")
	}
}

func TestStreamWriteReturnsCompletedByteCountOnError(t *testing.T) {
	wantErr := errors.New("write failed")
	writer := &recordingWriter{failCall: 2, failErr: wantErr}
	stream := mustNewStream(t, 3, writer, nil)
	data := make([]byte, protocol.MaxStreamDataSize+1)

	written, err := stream.Write(data)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write() error = %v, want %v", err, wantErr)
	}
	if written != protocol.MaxStreamDataSize {
		t.Fatalf("Write() wrote %d bytes, want %d", written, protocol.MaxStreamDataSize)
	}
	if frames := writer.snapshot(); len(frames) != 1 {
		t.Fatalf("successful frame count = %d, want 1", len(frames))
	}
}

func TestStreamReadBuffersAndDrainsBeforeEOF(t *testing.T) {
	stream := mustNewStream(t, 1, &recordingWriter{}, nil)

	if err := stream.deliver([]byte("abcdef")); err != nil {
		t.Fatalf("deliver(first) error = %v", err)
	}
	if err := stream.deliver([]byte("gh")); err != nil {
		t.Fatalf("deliver(second) error = %v", err)
	}
	stream.remoteClose()

	var got bytes.Buffer
	buffer := make([]byte, 3)
	for {
		n, err := stream.Read(buffer)
		got.Write(buffer[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
	}

	if got.String() != "abcdefgh" {
		t.Fatalf("read data = %q, want %q", got.String(), "abcdefgh")
	}
}

func TestStreamCloseInterruptsReadAndIsIdempotent(t *testing.T) {
	writer := &recordingWriter{}
	var closeCalls atomic.Int32
	stream := mustNewStream(t, 12, writer, func(id protocol.StreamID) {
		if id != 12 {
			t.Errorf("onClose ID = %d, want 12", id)
		}
		closeCalls.Add(1)
	})

	readResult := make(chan error, 1)
	go func() {
		_, err := stream.Read(make([]byte, 1))
		readResult <- err
	}()

	const closers = 8
	var wg sync.WaitGroup
	wg.Add(closers)
	for range closers {
		go func() {
			defer wg.Done()
			if err := stream.Close(); err != nil {
				t.Errorf("Close() error = %v", err)
			}
		}()
	}
	wg.Wait()

	select {
	case err := <-readResult:
		if !errors.Is(err, ErrStreamClosed) {
			t.Fatalf("Read() error = %v, want ErrStreamClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read() did not return after Close()")
	}

	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("onClose call count = %d, want 1", got)
	}

	frames := writer.snapshot()
	if len(frames) != 1 || frames[0].typ != protocol.TypeCloseStream {
		t.Fatalf("frames = %#v, want one CLOSE_STREAM", frames)
	}
	id, err := protocol.DecodeStreamID(frames[0].payload)
	if err != nil {
		t.Fatalf("DecodeStreamID() error = %v", err)
	}
	if id != stream.ID() {
		t.Fatalf("CLOSE_STREAM ID = %d, want %d", id, stream.ID())
	}
}

func TestStreamCloseUnblocksFullInboundQueue(t *testing.T) {
	stream := mustNewStream(t, 5, &recordingWriter{}, nil)
	for range streamInboundQueueSize {
		if err := stream.deliver([]byte("x")); err != nil {
			t.Fatalf("deliver() error = %v", err)
		}
	}

	deliverResult := make(chan error, 1)
	go func() {
		deliverResult <- stream.deliver([]byte("blocked"))
	}()

	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case err := <-deliverResult:
		if !errors.Is(err, ErrStreamClosed) {
			t.Fatalf("deliver() error = %v, want ErrStreamClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("deliver() remained blocked after Close()")
	}
}

func TestRemoteCloseRejectsWritesAndSuppressesCloseFrame(t *testing.T) {
	writer := &recordingWriter{}
	var closeCalls atomic.Int32
	stream := mustNewStream(t, 4, writer, func(protocol.StreamID) {
		closeCalls.Add(1)
	})
	stream.remoteClose()

	if n, err := stream.Write([]byte("data")); n != 0 || !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Write() = (%d, %v), want (0, ErrStreamClosed)", n, err)
	}
	if err := stream.deliver([]byte("data")); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("deliver() error = %v, want ErrStreamClosed", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if frames := writer.snapshot(); len(frames) != 0 {
		t.Fatalf("frame count = %d, want 0", len(frames))
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("onClose call count = %d, want 1", got)
	}
}

func TestStreamCloseWaitsForActiveWrite(t *testing.T) {
	writer := &recordingWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	stream := mustNewStream(t, 20, writer, nil)
	data := make([]byte, protocol.MaxStreamDataSize+1)

	type writeResult struct {
		n   int
		err error
	}
	writeDone := make(chan writeResult, 1)
	go func() {
		n, err := stream.Write(data)
		writeDone <- writeResult{n: n, err: err}
	}()

	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("WriteFrame() did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- stream.Close() }()

	select {
	case <-stream.localDone:
	case <-time.After(time.Second):
		t.Fatal("Close() did not mark the stream closed")
	}

	select {
	case err := <-closeDone:
		t.Fatalf("Close() returned before active write completed: %v", err)
	default:
	}

	close(writer.release)

	result := <-writeDone
	if result.n != protocol.MaxStreamDataSize || !errors.Is(result.err, ErrStreamClosed) {
		t.Fatalf("Write() = (%d, %v), want (%d, ErrStreamClosed)", result.n, result.err, protocol.MaxStreamDataSize)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	frames := writer.snapshot()
	if len(frames) != 2 {
		t.Fatalf("frame count = %d, want 2", len(frames))
	}
	if frames[0].typ != protocol.TypeStreamData || frames[1].typ != protocol.TypeCloseStream {
		t.Fatalf("frame types = [%v, %v], want [STREAM_DATA, CLOSE_STREAM]", frames[0].typ, frames[1].typ)
	}
}

func TestStreamRejectsEmptyDeliveryAndAcceptsEmptyIO(t *testing.T) {
	writer := &recordingWriter{}
	stream := mustNewStream(t, 2, writer, nil)

	if err := stream.deliver(nil); !errors.Is(err, protocol.ErrMalformedStreamPayload) {
		t.Fatalf("deliver(nil) error = %v, want ErrMalformedStreamPayload", err)
	}
	if n, err := stream.Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := stream.Write(nil); n != 0 || err != nil {
		t.Fatalf("Write(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if frames := writer.snapshot(); len(frames) != 0 {
		t.Fatalf("frame count = %d, want 0", len(frames))
	}
}

func mustNewStream(t *testing.T, id protocol.StreamID, writer frameWriter, onClose func(protocol.StreamID)) *Stream {
	t.Helper()

	stream, err := newStream(id, writer, onClose)
	if err != nil {
		t.Fatalf("newStream() error = %v", err)
	}
	return stream
}
