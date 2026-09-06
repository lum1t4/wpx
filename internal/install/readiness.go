package install

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/lum1t4/wpx/internal/config"
)

// WaitForPanel verifies that the HTTPS listener and its database are usable.
// Both installation and upgrade require this after systemd starts the process:
// Type=simple alone cannot detect a failure while loading keys or opening state.
func WaitForPanel(ctx context.Context, cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("parse panel listen address: %w", err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid panel port %q", port)
	}
	// The initial certificate is self-signed and domain access can use a
	// certificate whose name differs from the local listener. This probe sends
	// no credentials and deliberately bypasses proxy environment variables.
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	endpoint := "https://" + net.JoinHostPort(host, port) + "/healthz"
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		response, requestErr := client.Do(request)
		lastErr = requestErr
		if requestErr == nil {
			_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if response.StatusCode == http.StatusOK && readErr == nil {
				return nil
			}
			lastErr = fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
			if readErr != nil {
				lastErr = readErr
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for panel readiness (%v): %w", lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}
