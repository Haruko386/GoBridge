package tunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

type managerTestWriter struct {
	frames chan protocol.Frame
	err    error
}

func newManagerTestWriter() *managerTestWriter {
	return &managerTestWriter{
		frames: make(chan protocol.Frame, 32),
	}
}

func (w *managerTestWriter) WriteFrame(typ protocol.MessageType, payload []byte) error {
	if w.err != nil {
		return w.err
	}

	w.frames <- protocol.Frame{
		Type:    typ,
		Payload: bytes.Clone(payload),
	}
	return nil
}

type openResult struct {
	stream *Stream
	err    error
}

func TestNewManagerValidatesArguments(t *testing.T) {
	writer := newManagerTestWriter()

	if _, err := NewManager(nil, FirstServerStreamID, nil); err == nil {
		t.Fatal("NewManager(nil writer) error = nil, want an error")
	}

	for _, firstID := range []protocol.StreamID{0, 3, 4} {
		if _, err := NewManager(writer, firstID, nil); err == nil {
			t.Fatalf("NewManager(first ID %d) error = nil, want an error", firstID)
		}
	}

	for _, firstID := range []protocol.StreamID{
		FirstServerStreamID,
		FirstClientStreamID,
	} {
		manager, err := NewManager(writer, firstID, nil)
		if err != nil {
			t.Fatalf("NewManager(first ID %d) error = %v", firstID, err)
		}
		if err := manager.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}
}

func TestManagerOpenAllocatesIDsAndHandlesOpenOK(t *testing.T) {
	writer := newManagerTestWriter()
	manager := mustNewManager(t, writer, FirstServerStreamID, nil)
	defer manager.Close()

	firstResult := startOpen(manager)
	firstRequest := nextManagerFrame(t, writer)
	firstID := decodeFrameStreamID(t, firstRequest, protocol.TypeOpenStream)
	if firstID != 1 {
		t.Fatalf("first stream ID = %d, want 1", firstID)
	}

	firstOK, err := protocol.EncodeStreamID(firstID)
	if err != nil {
		t.Fatalf("EncodeStreamID(first) error = %v", err)
	}
	if err := manager.HandleFrame(protocol.Frame{
		Type:    protocol.TypeOpenOK,
		Payload: firstOK,
	}); err != nil {
		t.Fatalf("HandleFrame(first OPEN_OK) error = %v", err)
	}
	firstStream := requireOpenSuccess(t, firstResult)

	secondResult := startOpen(manager)
	secondRequest := nextManagerFrame(t, writer)
	secondID := decodeFrameStreamID(t, secondRequest, protocol.TypeOpenStream)
	if secondID != 3 {
		t.Fatalf("second stream ID = %d, want 3", secondID)
	}

	secondOK, err := protocol.EncodeStreamID(secondID)
	if err != nil {
		t.Fatalf("EncodeStreamID(second) error = %v", err)
	}
	if err := manager.HandleFrame(protocol.Frame{
		Type:    protocol.TypeOpenOK,
		Payload: secondOK,
	}); err != nil {
		t.Fatalf("HandleFrame(second OPEN_OK) error = %v", err)
	}
	secondStream := requireOpenSuccess(t, secondResult)

	if firstStream.ID() != firstID || secondStream.ID() != secondID {
		t.Fatalf(
			"opened IDs = (%d, %d), want (%d, %d)",
			firstStream.ID(),
			secondStream.ID(),
			firstID,
			secondID,
		)
	}
	if manager.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", manager.Len())
	}
}

func TestManagerOpenHandlesRejection(t *testing.T) {
	writer := newManagerTestWriter()
	manager := mustNewManager(t, writer, FirstServerStreamID, nil)
	defer manager.Close()

	result := startOpen(manager)
	request := nextManagerFrame(t, writer)
	id := decodeFrameStreamID(t, request, protocol.TypeOpenStream)

	payload, err := protocol.EncodeOpenFailed(id, "upstream unavailable")
	if err != nil {
		t.Fatalf("EncodeOpenFailed() error = %v", err)
	}
	if err := manager.HandleFrame(protocol.Frame{
		Type:    protocol.TypeOpenFailed,
		Payload: payload,
	}); err != nil {
		t.Fatalf("HandleFrame(OPEN_FAILED) error = %v", err)
	}

	open := nextOpenResult(t, result)
	if open.stream != nil {
		t.Fatal("Open() returned a stream after rejection")
	}
	if !errors.Is(open.err, ErrStreamOpenRejected) {
		t.Fatalf("Open() error = %v, want ErrStreamOpenRejected", open.err)
	}

	var rejected *OpenRejectedError
	if !errors.As(open.err, &rejected) {
		t.Fatalf("Open() error type = %T, want *OpenRejectedError", open.err)
	}
	if rejected.StreamID != id || rejected.Reason != "upstream unavailable" {
		t.Fatalf("rejection = %#v", rejected)
	}
	if manager.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", manager.Len())
	}
}

func TestManagerOpenCancellationSendsClose(t *testing.T) {
	writer := newManagerTestWriter()
	manager := mustNewManager(t, writer, FirstServerStreamID, nil)
	defer manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan openResult, 1)
	go func() {
		stream, err := manager.Open(ctx)
		result <- openResult{stream: stream, err: err}
	}()

	request := nextManagerFrame(t, writer)
	id := decodeFrameStreamID(t, request, protocol.TypeOpenStream)
	cancel()

	open := nextOpenResult(t, result)
	if !errors.Is(open.err, context.Canceled) {
		t.Fatalf("Open() error = %v, want context.Canceled", open.err)
	}

	closeFrame := nextManagerFrame(t, writer)
	closeID := decodeFrameStreamID(t, closeFrame, protocol.TypeCloseStream)
	if closeID != id {
		t.Fatalf("CLOSE_STREAM ID = %d, want %d", closeID, id)
	}
	if manager.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", manager.Len())
	}
}

func TestManagerCloseWakesPendingOpen(t *testing.T) {
	writer := newManagerTestWriter()
	manager := mustNewManager(t, writer, FirstServerStreamID, nil)

	result := startOpen(manager)
	_ = nextManagerFrame(t, writer)

	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	open := nextOpenResult(t, result)
	if !errors.Is(open.err, ErrManagerClosed) {
		t.Fatalf("Open() error = %v, want ErrManagerClosed", open.err)
	}
	if manager.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", manager.Len())
	}

	if _, err := manager.Open(context.Background()); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Open() after Close() error = %v, want ErrManagerClosed", err)
	}
	if err := manager.HandleFrame(protocol.Frame{Type: protocol.TypeCloseStream}); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("HandleFrame() after Close() error = %v, want ErrManagerClosed", err)
	}
}

func TestManagerAcceptsIncomingStreamAndRoutesFrames(t *testing.T) {
	writer := newManagerTestWriter()
	incoming := make(chan *Stream, 1)
	manager := mustNewManager(
		t,
		writer,
		FirstClientStreamID,
		func(stream *Stream) { incoming <- stream },
	)
	defer manager.Close()

	openPayload, err := protocol.EncodeStreamID(1)
	if err != nil {
		t.Fatalf("EncodeStreamID() error = %v", err)
	}
	if err := manager.HandleFrame(protocol.Frame{
		Type:    protocol.TypeOpenStream,
		Payload: openPayload,
	}); err != nil {
		t.Fatalf("HandleFrame(OPEN_STREAM) error = %v", err)
	}

	openOK := nextManagerFrame(t, writer)
	if id := decodeFrameStreamID(t, openOK, protocol.TypeOpenOK); id != 1 {
		t.Fatalf("OPEN_OK ID = %d, want 1", id)
	}

	var stream *Stream
	select {
	case stream = <-incoming:
	case <-time.After(time.Second):
		t.Fatal("incoming handler was not called")
	}

	dataPayload, err := protocol.EncodeStreamData(1, []byte("hello"))
	if err != nil {
		t.Fatalf("EncodeStreamData() error = %v", err)
	}
	if err := manager.HandleFrame(protocol.Frame{
		Type:    protocol.TypeStreamData,
		Payload: dataPayload,
	}); err != nil {
		t.Fatalf("HandleFrame(STREAM_DATA) error = %v", err)
	}

	buffer := make([]byte, 5)
	if _, err := io.ReadFull(stream, buffer); err != nil {
		t.Fatalf("ReadFull() error = %v", err)
	}
	if string(buffer) != "hello" {
		t.Fatalf("stream data = %q, want %q", buffer, "hello")
	}

	closePayload, err := protocol.EncodeStreamID(1)
	if err != nil {
		t.Fatalf("EncodeStreamID(close) error = %v", err)
	}
	if err := manager.HandleFrame(protocol.Frame{
		Type:    protocol.TypeCloseStream,
		Payload: closePayload,
	}); err != nil {
		t.Fatalf("HandleFrame(CLOSE_STREAM) error = %v", err)
	}

	if n, err := stream.Read(buffer); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read() after remote close = (%d, %v), want (0, EOF)", n, err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("stream.Close() error = %v", err)
	}
	if manager.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", manager.Len())
	}
}

func TestManagerRejectsUnsupportedIncomingStream(t *testing.T) {
	writer := newManagerTestWriter()
	manager := mustNewManager(t, writer, FirstServerStreamID, nil)
	defer manager.Close()

	payload, err := protocol.EncodeStreamID(2)
	if err != nil {
		t.Fatalf("EncodeStreamID() error = %v", err)
	}
	if err := manager.HandleFrame(protocol.Frame{
		Type:    protocol.TypeOpenStream,
		Payload: payload,
	}); err != nil {
		t.Fatalf("HandleFrame(OPEN_STREAM) error = %v", err)
	}

	failed := nextManagerFrame(t, writer)
	if failed.Type != protocol.TypeOpenFailed {
		t.Fatalf("response type = %s, want OPEN_FAILED", failed.Type)
	}
	id, reason, err := protocol.DecodeOpenFailed(failed.Payload)
	if err != nil {
		t.Fatalf("DecodeOpenFailed() error = %v", err)
	}
	if id != 2 || reason != "incoming streams are not supported" {
		t.Fatalf("OPEN_FAILED = (%d, %q)", id, reason)
	}
}

func TestManagerRejectsWrongEndpointStreamID(t *testing.T) {
	writer := newManagerTestWriter()
	manager := mustNewManager(
		t,
		writer,
		FirstClientStreamID,
		func(*Stream) {},
	)
	defer manager.Close()

	payload, err := protocol.EncodeStreamID(2)
	if err != nil {
		t.Fatalf("EncodeStreamID() error = %v", err)
	}
	if err := manager.HandleFrame(protocol.Frame{
		Type:    protocol.TypeOpenStream,
		Payload: payload,
	}); err != nil {
		t.Fatalf("HandleFrame(OPEN_STREAM) error = %v", err)
	}

	failed := nextManagerFrame(t, writer)
	id, reason, err := protocol.DecodeOpenFailed(failed.Payload)
	if err != nil {
		t.Fatalf("DecodeOpenFailed() error = %v", err)
	}
	if id != 2 || reason != "stream ID belongs to the receiving endpoint" {
		t.Fatalf("OPEN_FAILED = (%d, %q)", id, reason)
	}
}

func TestManagerIgnoresLateFramesForClosedStreams(t *testing.T) {
	writer := newManagerTestWriter()
	manager := mustNewManager(t, writer, FirstServerStreamID, nil)
	defer manager.Close()

	idPayload, err := protocol.EncodeStreamID(1)
	if err != nil {
		t.Fatalf("EncodeStreamID() error = %v", err)
	}
	dataPayload, err := protocol.EncodeStreamData(1, []byte("late"))
	if err != nil {
		t.Fatalf("EncodeStreamData() error = %v", err)
	}

	frames := []protocol.Frame{
		{Type: protocol.TypeOpenOK, Payload: idPayload},
		{Type: protocol.TypeOpenFailed, Payload: mustEncodeOpenFailed(t, 1, "late")},
		{Type: protocol.TypeStreamData, Payload: dataPayload},
		{Type: protocol.TypeCloseStream, Payload: idPayload},
	}
	for _, frame := range frames {
		if err := manager.HandleFrame(frame); err != nil {
			t.Fatalf("HandleFrame(%s) error = %v", frame.Type, err)
		}
	}
}

func TestManagerRejectsMalformedAndNonTunnelFrames(t *testing.T) {
	manager := mustNewManager(
		t,
		newManagerTestWriter(),
		FirstServerStreamID,
		nil,
	)
	defer manager.Close()

	if err := manager.HandleFrame(protocol.Frame{Type: protocol.TypePing}); !errors.Is(err, ErrUnexpectedTunnelFrame) {
		t.Fatalf("HandleFrame(PING) error = %v, want ErrUnexpectedTunnelFrame", err)
	}

	for _, typ := range []protocol.MessageType{
		protocol.TypeOpenStream,
		protocol.TypeOpenOK,
		protocol.TypeOpenFailed,
		protocol.TypeStreamData,
		protocol.TypeCloseStream,
	} {
		if err := manager.HandleFrame(protocol.Frame{Type: typ}); err == nil {
			t.Fatalf("HandleFrame(malformed %s) error = nil", typ)
		}
	}
}

func TestManagerReportsWriterFailureAndIDExhaustion(t *testing.T) {
	wantErr := errors.New("writer failed")
	writer := newManagerTestWriter()
	writer.err = wantErr
	manager := mustNewManager(t, writer, FirstServerStreamID, nil)

	if _, err := manager.Open(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Open() error = %v, want writer error", err)
	}
	if manager.Len() != 0 {
		t.Fatalf("Len() after writer failure = %d, want 0", manager.Len())
	}

	manager.mu.Lock()
	manager.nextID = 0
	manager.mu.Unlock()

	if _, err := manager.Open(context.Background()); !errors.Is(err, ErrStreamIDsExhausted) {
		t.Fatalf("Open() error = %v, want ErrStreamIDsExhausted", err)
	}
}

func startOpen(manager *Manager) <-chan openResult {
	result := make(chan openResult, 1)
	go func() {
		stream, err := manager.Open(context.Background())
		result <- openResult{stream: stream, err: err}
	}()
	return result
}

func nextOpenResult(t *testing.T, result <-chan openResult) openResult {
	t.Helper()

	select {
	case value := <-result:
		return value
	case <-time.After(time.Second):
		t.Fatal("Open() did not return")
		return openResult{}
	}
}

func requireOpenSuccess(t *testing.T, result <-chan openResult) *Stream {
	t.Helper()

	value := nextOpenResult(t, result)
	if value.err != nil {
		t.Fatalf("Open() error = %v", value.err)
	}
	if value.stream == nil {
		t.Fatal("Open() returned a nil stream")
	}
	return value.stream
}

func nextManagerFrame(t *testing.T, writer *managerTestWriter) protocol.Frame {
	t.Helper()

	select {
	case frame := <-writer.frames:
		return frame
	case <-time.After(time.Second):
		t.Fatal("writer did not receive a frame")
		return protocol.Frame{}
	}
}

func decodeFrameStreamID(
	t *testing.T,
	frame protocol.Frame,
	wantType protocol.MessageType,
) protocol.StreamID {
	t.Helper()

	if frame.Type != wantType {
		t.Fatalf("frame type = %s, want %s", frame.Type, wantType)
	}

	id, err := protocol.DecodeStreamID(frame.Payload)
	if err != nil {
		t.Fatalf("DecodeStreamID() error = %v", err)
	}
	return id
}

func mustNewManager(
	t *testing.T,
	writer frameWriter,
	firstID protocol.StreamID,
	incoming IncomingHandler,
) *Manager {
	t.Helper()

	manager, err := NewManager(writer, firstID, incoming)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func mustEncodeOpenFailed(
	t *testing.T,
	id protocol.StreamID,
	reason string,
) []byte {
	t.Helper()

	payload, err := protocol.EncodeOpenFailed(id, reason)
	if err != nil {
		t.Fatalf("EncodeOpenFailed() error = %v", err)
	}
	return payload
}
