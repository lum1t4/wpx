package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestDomainClientRecoveryClassification(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		result    string
		typed     bool
		restored  bool
		uncertain bool
	}{
		{name: "plain rejection"},
		{name: "restored", result: `{"previous_restored":true}`, typed: true, restored: true},
		{name: "not restored", result: `{"previous_restored":false}`, typed: true},
		{name: "wrong type", result: `{"previous_restored":"true"}`, uncertain: true},
		{name: "missing field", result: `{}`, uncertain: true},
		{name: "null field", result: `{"previous_restored":null}`, uncertain: true},
		{name: "null result", result: `null`, uncertain: true},
		{name: "unknown field", result: `{"previous_restored":true,"unrecognized":true}`, uncertain: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			err := lifecycleClientResponse(t, OpChangeDomain, func(request Request) *Response {
				return &Response{Version: ProtocolVersion, ID: request.ID, Error: "host failure", Result: json.RawMessage(scenario.result)}
			})
			if err == nil || errors.Is(err, ErrOutcomeUnknown) != scenario.uncertain {
				t.Fatalf("wrong failure classification: %v", err)
			}
			var changeErr *model.DomainChangeError
			if errors.As(err, &changeErr) != scenario.typed {
				t.Fatalf("wrong typed error: %v", err)
			}
			if changeErr != nil && changeErr.PreviousRestored != scenario.restored {
				t.Fatalf("wrong recovery state: %+v", changeErr)
			}
		})
	}
}

func TestLifecycleClientPreservesUnknownTransportOutcome(t *testing.T) {
	for _, operation := range []Operation{OpChangeDomain, OpDeleteSite} {
		for _, scenario := range []string{"disconnected", "mismatched ID", "mismatched version", "success"} {
			t.Run(string(operation)+"/"+scenario, func(t *testing.T) {
				err := lifecycleClientResponse(t, operation, func(request Request) *Response {
					if scenario == "disconnected" {
						return nil
					}
					response := &Response{Version: ProtocolVersion, ID: request.ID, OK: true}
					if scenario == "mismatched ID" {
						response.ID = "another-request"
					}
					if scenario == "mismatched version" {
						response.Version++
					}
					return response
				})
				if scenario == "success" {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, ErrOutcomeUnknown) {
					t.Fatalf("lost response must retain the durable reservation: %v", err)
				}
			})
		}
		client := Client{SocketPath: filepath.Join(t.TempDir(), "absent.sock")}
		if err := client.Call(context.Background(), operation, "durable-site-job", struct{}{}, nil); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unavailable broker must be retryable: %v", err)
		}
	}
}

func TestLifecycleClientRejectsInvalidKeyBeforeDialing(t *testing.T) {
	client := Client{SocketPath: filepath.Join(t.TempDir(), "absent.sock")}
	for _, operation := range []Operation{OpChangeDomain, OpDeleteSite} {
		for _, key := range []string{"", " \t\n", strings.Repeat("x", 161)} {
			if err := client.Call(context.Background(), operation, key, struct{}{}, nil); err == nil || errors.Is(err, ErrUnavailable) {
				t.Fatalf("invalid key reached the connection: %v", err)
			}
		}
	}
}

func lifecycleClientResponse(t *testing.T, operation Operation, respond func(Request) *Response) error {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		var request Request
		if err := json.NewDecoder(connection).Decode(&request); err != nil {
			done <- err
			return
		}
		if request.Operation != operation || request.IdempotencyKey != "durable-site-job" {
			done <- errors.New("client lost operation or durable idempotency key")
			return
		}
		response := respond(request)
		if response == nil {
			done <- nil
			return
		}
		done <- json.NewEncoder(connection).Encode(response)
	}()
	client := Client{SocketPath: socket, Timeout: time.Second}
	err = client.Call(context.Background(), operation, "durable-site-job", struct{}{}, nil)
	if serverErr := <-done; serverErr != nil {
		t.Fatal(serverErr)
	}
	return err
}
