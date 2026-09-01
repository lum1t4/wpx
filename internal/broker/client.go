package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

type Client struct {
	SocketPath string
	Timeout    time.Duration
}

func (c Client) Call(ctx context.Context, operation Operation, idempotencyKey string, payload any, result any) error {
	if idempotencyKey == "" {
		return errors.New("idempotency key is required")
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return fmt.Errorf("connect to broker: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request := Request{
		Version: ProtocolVersion, ID: requestID(), IdempotencyKey: idempotencyKey,
		Operation: operation, Payload: raw,
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return fmt.Errorf("send broker request: %w", err)
	}
	var response Response
	if err := json.NewDecoder(bufio.NewReader(ioLimitReader(conn, maxRequestBytes))).Decode(&response); err != nil {
		return fmt.Errorf("read broker response: %w", err)
	}
	if response.Version != ProtocolVersion || response.ID != request.ID {
		return errors.New("broker response does not match request")
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	if result != nil && len(response.Result) != 0 {
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode broker result: %w", err)
		}
	}
	return nil
}

func requestID() string {
	// Request IDs are correlation values, not authentication tokens. Time plus
	// process-local monotonicity is unnecessary because idempotency keys carry the
	// durable uniqueness requirement.
	return fmt.Sprintf("req_%x", time.Now().UnixNano())
}
