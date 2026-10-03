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

	closeEndpoints := func() {
		closeOnce.Do(func() {
			var closeGroup sync.WaitGroup
			closeGroup.Add(2)

			go func() {
				defer closeGroup.Done()
				_ = left.Close()
			}()

			go func() {
				defer closeGroup.Done()
				_ = right.Close()
			}()

			closeGroup.Wait()
		})
	}

	stopCancelClose := context.AfterFunc(ctx, closeEndpoints)
	defer stopCancelClose()

	go copyDirection("left to right", right, left, results)

	go copyDirection("right to left", left, right, results)

	first := <-results

	closeEndpoints()

	second := <-results

	if ctx.Err() != nil {
		return nil
	}

	return errors.Join(normalizeCopyError(first), normalizeCopyError(second))
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
