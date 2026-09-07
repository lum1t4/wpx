package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestClientCancellationInterruptsPendingReply(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var request Request
		if json.NewDecoder(connection).Decode(&request) != nil {
			return
		}
		close(received)
		var b [1]byte
		_, _ = connection.Read(b[:])
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- (Client{SocketPath: socket, Timeout: 30 * time.Second}).Call(ctx, OpProbe, "cancel-test", struct{}{}, nil)
	}()
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("broker did not receive request")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("cancellation after sending must preserve unknown outcome: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not interrupt the pending reply")
	}
	<-closed
}
