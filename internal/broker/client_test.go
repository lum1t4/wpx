package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestClientFailureOutcomes(t *testing.T) {
	for _, scenario := range []string{"disconnected", "mismatched", "rejected", "rolled back", "bad recovery"} {
		t.Run(scenario, func(t *testing.T) {
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
				response := Response{Version: ProtocolVersion, ID: request.ID, Error: "operation failed"}
				switch scenario {
				case "disconnected":
					done <- nil
					return
				case "mismatched":
					response.ID = "different-request"
				case "rolled back":
					response.Result = json.RawMessage(`{"previous_restored":true}`)
				case "bad recovery":
					response.Result = json.RawMessage(`{"previous_restored":"invalid"}`)
				}
				done <- json.NewEncoder(connection).Encode(response)
			}()
			client := Client{SocketPath: socket, Timeout: time.Second}
			err = client.Call(context.Background(), OpChangePHPVersion, "same-durable-job", struct{}{}, nil)
			if serverErr := <-done; serverErr != nil {
				t.Fatal(serverErr)
			}
			if err == nil {
				t.Fatal("failed broker response was reported as success")
			}
			uncertain := scenario == "disconnected" || scenario == "mismatched" || scenario == "bad recovery"
			if errors.Is(err, ErrOutcomeUnknown) != uncertain {
				t.Fatalf("wrong outcome classification for %s: %v", scenario, err)
			}
			var recovery *model.PHPVersionChangeError
			if scenario == "rolled back" && (!errors.As(err, &recovery) || !recovery.PreviousRestored) {
				t.Fatalf("confirmed recovery was lost in transport: %v", err)
			}
		})
	}
	client := Client{SocketPath: filepath.Join(t.TempDir(), "absent.sock")}
	if err := client.Call(context.Background(), OpChangePHPVersion, "same-durable-job", struct{}{}, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable broker must retain a retried operation's reservation: %v", err)
	}
}
