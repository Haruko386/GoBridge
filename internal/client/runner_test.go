package client

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/peer"
	"github.com/Haruko386/GoBridge/internal/transport"
	"github.com/Haruko386/GoBridge/internal/tunnel"
)

func TestNewRunnerValidatesConfiguration(t *testing.T) {
	validConnect := func(context.Context) (HeartbeatSession, error) { return nil, errors.New("unused") }
	validSession := func(context.Context, HeartbeatSession, time.Duration, time.Duration) error { return nil }

	if _, err := NewRunner(nil, time.Second, time.Second, time.Second, time.Second, nil); err == nil {
		t.Fatal("NewRunner(nil connector) error = nil")
	}

	tests := []struct {
		name              string
		connect           connectSessionFunc
		runSession        runSessionFunc
		heartbeatInterval time.Duration
		heartbeatTimeout  time.Duration
		minBackoff        time.Duration
		maxBackoff        time.Duration
	}{
		{name: "nil connect", runSession: validSession, heartbeatInterval: time.Second, heartbeatTimeout: time.Second, minBackoff: time.Second, maxBackoff: time.Second},
		{name: "nil session", connect: validConnect, heartbeatInterval: time.Second, heartbeatTimeout: time.Second, minBackoff: time.Second, maxBackoff: time.Second},
		{name: "zero heartbeat interval", connect: validConnect, runSession: validSession, heartbeatTimeout: time.Second, minBackoff: time.Second, maxBackoff: time.Second},
		{name: "zero heartbeat timeout", connect: validConnect, runSession: validSession, heartbeatInterval: time.Second, minBackoff: time.Second, maxBackoff: time.Second},
		{name: "zero min backoff", connect: validConnect, runSession: validSession, heartbeatInterval: time.Second, heartbeatTimeout: time.Second, maxBackoff: time.Second},
		{name: "zero max backoff", connect: validConnect, runSession: validSession, heartbeatInterval: time.Second, heartbeatTimeout: time.Second, minBackoff: time.Second},
		{name: "max below min", connect: validConnect, runSession: validSession, heartbeatInterval: time.Second, heartbeatTimeout: time.Second, minBackoff: 2 * time.Second, maxBackoff: time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newRunner(
				test.connect,
				test.runSession,
				test.heartbeatInterval,
				test.heartbeatTimeout,
				test.minBackoff,
				test.maxBackoff,
				nil,
			); err == nil {
				t.Fatal("newRunner() error = nil")
			}
		})
	}
}

func TestNewTunnelRunnerValidatesDependencies(t *testing.T) {
	forwarder, err := NewProxyForwarder("127.0.0.1:7897", time.Second)
	if err != nil {
		t.Fatalf("NewProxyForwarder() error = %v", err)
	}

	if _, err := NewTunnelRunner(nil, forwarder, time.Second, time.Second, time.Second, time.Second, nil); err == nil {
		t.Fatal("NewTunnelRunner(nil connector) error = nil")
	}
	if _, err := NewTunnelRunner(&Connector{}, nil, time.Second, time.Second, time.Second, time.Second, nil); err == nil {
		t.Fatal("NewTunnelRunner(nil forwarder) error = nil")
	}
}

func TestRunnerRetriesWithBackoffAndResetsAfterConnection(t *testing.T) {
	transientErr := errors.New("temporary network failure")
	heartbeatErr := errors.New("connection lost")
	session := newClientHeartbeatTestSession()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	attempt := 0
	runner, err := newRunner(
		func(context.Context) (HeartbeatSession, error) {
			attempt++
			switch attempt {
			case 1, 2, 4:
				return nil, transientErr
			case 3:
				return session, nil
			default:
				cancel()
				return nil, context.Canceled
			}
		},
		func(_ context.Context, got HeartbeatSession, interval, timeout time.Duration) error {
			if got != session {
				t.Errorf("heartbeat session = %v, want %v", got, session)
			}
			if interval != 10*time.Second || timeout != 3*time.Second {
				t.Errorf("heartbeat durations = (%s, %s), want (10s, 3s)", interval, timeout)
			}
			return heartbeatErr
		},
		10*time.Second,
		3*time.Second,
		time.Second,
		8*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}

	var delays []time.Duration
	runner.waitRetry = func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return true
	}
	var reported []error
	runner.reportError = func(err error) { reported = append(reported, err) }

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := delays, []time.Duration{time.Second, 2 * time.Second, time.Second, time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry delays = %v, want %v", got, want)
	}
	if len(reported) != 4 {
		t.Fatalf("reported errors = %d, want 4", len(reported))
	}
	if !errors.Is(reported[0], transientErr) || !errors.Is(reported[1], transientErr) || !errors.Is(reported[2], heartbeatErr) || !errors.Is(reported[3], transientErr) {
		t.Fatalf("reported errors = %v, want connect/connect/heartbeat/connect errors", reported)
	}
}

func TestRunnerStopsOnPermanentConnectionErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "disabled peer", err: ErrServerPeerDisabled},
		{name: "removed peer", err: peer.ErrNotFound},
		{name: "unexpected server", err: ErrUnexpectedServer},
		{name: "TLS identity mismatch", err: transport.ErrServerIdentityMismatch},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			runner, err := newRunner(
				func(context.Context) (HeartbeatSession, error) {
					attempts++
					return nil, test.err
				},
				func(context.Context, HeartbeatSession, time.Duration, time.Duration) error {
					t.Fatal("heartbeat called after permanent connection error")
					return nil
				},
				time.Second,
				time.Second,
				time.Second,
				time.Second,
				func(error) { t.Fatal("permanent error was reported as transient") },
			)
			if err != nil {
				t.Fatalf("newRunner() error = %v", err)
			}
			runner.waitRetry = func(context.Context, time.Duration) bool {
				t.Fatal("retry wait called after permanent error")
				return false
			}

			err = runner.Run(context.Background())
			if !errors.Is(err, test.err) {
				t.Fatalf("Run() error = %v, want %v", err, test.err)
			}
			if attempts != 1 {
				t.Fatalf("connection attempts = %d, want 1", attempts)
			}
		})
	}
}

func TestRunnerCancellationInterruptsBackoff(t *testing.T) {
	transientErr := errors.New("offline")
	reported := make(chan error, 1)
	runner, err := newRunner(
		func(context.Context) (HeartbeatSession, error) { return nil, transientErr },
		func(context.Context, HeartbeatSession, time.Duration, time.Duration) error { return nil },
		time.Second,
		time.Second,
		time.Hour,
		time.Hour,
		func(err error) { reported <- err },
	)
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runner.Run(ctx) }()
	clientHeartbeatReceive(t, reported, "connection error report")
	cancel()
	if err := clientHeartbeatReceive(t, result, "runner cancellation"); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

func TestRunnerRejectsNilContext(t *testing.T) {
	runner, err := newRunner(
		func(context.Context) (HeartbeatSession, error) { return nil, errors.New("unused") },
		func(context.Context, HeartbeatSession, time.Duration, time.Duration) error { return nil },
		time.Second,
		time.Second,
		time.Second,
		time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}
	if err := runner.Run(nil); err == nil {
		t.Fatal("Run(nil) error = nil")
	}
}

func TestTunnelRuntimeStopsWhenServerPeerIsDisabled(t *testing.T) {
	dir := t.TempDir()
	writer, err := peer.Open(dir)
	if err != nil {
		t.Fatalf("peer.Open(writer) error = %v", err)
	}
	serverIdentity := connectorTestIdentity(t)
	addConnectorPeer(t, writer, "server", serverIdentity)
	reader, err := peer.Open(dir)
	if err != nil {
		t.Fatalf("peer.Open(reader) error = %v", err)
	}

	connector := &Connector{peers: reader, serverNodeID: serverIdentity.NodeID()}
	session := newClientHeartbeatTestSession()
	manager, err := tunnel.NewManager(session, tunnel.FirstClientStreamID, func(*tunnel.Stream) {})
	if err != nil {
		t.Fatalf("tunnel.NewManager() error = %v", err)
	}
	runtime, err := tunnel.NewRuntime(session, manager, time.Hour, time.Second)
	if err != nil {
		t.Fatalf("tunnel.NewRuntime() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runTunnelRuntime(ctx, cancel, runtime, connector, func(error) {}) }()

	if err := writer.Disable(serverIdentity.NodeID()); err != nil {
		t.Fatalf("Store.Disable() error = %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrServerPeerDisabled) {
			t.Fatalf("runTunnelRuntime() error = %v, want ErrServerPeerDisabled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runTunnelRuntime() did not stop after peer disable")
	}
}

func TestNextBackoff(t *testing.T) {
	tests := []struct {
		current time.Duration
		maximum time.Duration
		want    time.Duration
	}{
		{current: time.Second, maximum: 30 * time.Second, want: 2 * time.Second},
		{current: 16 * time.Second, maximum: 30 * time.Second, want: 30 * time.Second},
		{current: 30 * time.Second, maximum: 30 * time.Second, want: 30 * time.Second},
		{current: time.Duration(1 << 62), maximum: time.Duration(1<<62) + 1, want: time.Duration(1<<62) + 1},
	}
	for _, test := range tests {
		if got := nextBackoff(test.current, test.maximum); got != test.want {
			t.Errorf("nextBackoff(%s, %s) = %s, want %s", test.current, test.maximum, got, test.want)
		}
	}
}
