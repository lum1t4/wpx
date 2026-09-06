package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

type Client struct {
	SocketPath string
	Timeout    time.Duration
}

var (
	ErrUnavailable = errors.New("broker is unavailable")
	// The peer can continue executing after the client loses its connection.
	// Durable callers must replay the same operation, not unlock its target and
	// permit an incompatible new request based on an assumed failure.
	ErrOutcomeUnknown = errors.New("broker operation outcome is unknown")
)

func (c Client) Call(ctx context.Context, operation Operation, idempotencyKey string, payload any, result any) error {
	if idempotencyKey == "" {
		return errors.New("idempotency key is required")
	}
	if (operation == OpChangeDomain || operation == OpDeleteSite) && !validLifecycleKey(idempotencyKey) {
		return errors.New("lifecycle idempotency key must contain 1 to 160 bytes")
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return fmt.Errorf("%w: connect: %w", ErrUnavailable, err)
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
		return fmt.Errorf("%w: send request: %w", ErrOutcomeUnknown, err)
	}
	var response Response
	if err := json.NewDecoder(bufio.NewReader(ioLimitReader(conn, maxRequestBytes))).Decode(&response); err != nil {
		return fmt.Errorf("%w: read response: %w", ErrOutcomeUnknown, err)
	}
	if response.Version != ProtocolVersion || response.ID != request.ID {
		return fmt.Errorf("%w: response does not match request", ErrOutcomeUnknown)
	}
	if !response.OK {
		if operation == OpChangeDomain && len(response.Result) != 0 {
			restored, err := decodeDomainRecovery(response.Result)
			if err != nil {
				return fmt.Errorf("%w: decode domain recovery state: %w", ErrOutcomeUnknown, err)
			}
			return &model.DomainChangeError{Err: errors.New(response.Error), PreviousRestored: restored}
		}
		if operation == OpChangePHPVersion && len(response.Result) != 0 {
			var recovery ChangePHPVersionResult
			if err := json.Unmarshal(response.Result, &recovery); err != nil {
				return fmt.Errorf("%w: decode PHP recovery state: %w", ErrOutcomeUnknown, err)
			}
			return &model.PHPVersionChangeError{Err: errors.New(response.Error), PreviousRestored: recovery.PreviousRestored}
		}
		return errors.New(response.Error)
	}
	if result != nil && len(response.Result) != 0 {
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("%w: decode result: %w", ErrOutcomeUnknown, err)
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
