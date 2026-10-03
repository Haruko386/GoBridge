package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

type Endpoint interface {
	io.Reader
	io.Writer
	io.Closer
}

type copyResult struct {
	direction string
	err       error
}

func Bridge(ctx context.Context, left, right Endpoint) error {
	if ctx == nil {
		return errors.New("bridge context is nil")
	}

	if left == nil {
		return errors.New("bridge left endpoint is nil")
	}
	if right == nil {
		return errors.New("bridge right endpoint is nil")
	}

	results := make(chan copyResult, 2)

	var closeOnce sync.Once
	var closeErr error

	closeEndpoints := func() error {
		closeOnce.Do(func() {
			closeResults := make(chan error, 2)

			go func() {
				closeResults <- left.Close()
			}()

			go func() {
				closeResults <- right.Close()
			}()

			closeErr = errors.Join(<-closeResults, <-closeResults)
		})

		return closeErr
	}

	stopCancelClose := context.AfterFunc(ctx, func() {
		_ = closeEndpoints()
	})
	defer stopCancelClose()

	go copyDirection("left to right", right, left, results)

	go copyDirection("right to left", left, right, results)

	first := <-results

	// io.Copy reports a successful source EOF as nil. In that case the
	// reverse direction must remain open so it can carry the response. A
	// non-nil error is terminal and closes both sides to unblock the peer.
	if first.err != nil {
		_ = closeEndpoints()
	}

	second := <-results
	endpointErr := closeEndpoints()

	if ctx.Err() != nil {
		return endpointErr
	}

	return errors.Join(
		normalizeCopyError(first),
		normalizeCopyError(second),
		endpointErr,
	)
}

func copyDirection(direction string, destination io.Writer, source io.Reader, results chan<- copyResult) {
	_, err := io.Copy(destination, source)

	results <- copyResult{
		direction: direction,
		err:       err,
	}
}

func normalizeCopyError(result copyResult) error {
	if result.err == nil {
		return nil
	}

	if errors.Is(result.err, io.EOF) || errors.Is(result.err, io.ErrClosedPipe) || errors.Is(result.err, net.ErrClosed) {
		return nil
	}

	return fmt.Errorf("copy %s: %w", result.direction, result.err)
}
