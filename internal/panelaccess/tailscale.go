package panelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const tailscaleTarget = "https+insecure://127.0.0.1:9443"

type outputRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

// The relevant subset of `tailscale serve status --json`. Other ports and
// named Services belong to the operator and are neither reset nor rewritten.
type serveStatus struct {
	TCP map[string]struct {
		HTTPS bool
	}
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy    string
			Path     string
			Text     string
			Redirect string
		}
	}
	AllowFunnel map[string]bool
	Foreground  map[string]serveStatus
}

func readTailscaleStatus(ctx context.Context, options Options) (serveStatus, error) {
	reader, ok := options.Runner.(outputRunner)
	if !ok {
		return serveStatus{}, errors.New("runner cannot inspect existing Tailscale Serve configuration")
	}
	output, err := reader.Output(ctx, options.TailscalePath, "serve", "status", "--json")
	if err != nil {
		return serveStatus{}, fmt.Errorf("inspect Tailscale Serve (install and authenticate Tailscale first): %w", err)
	}
	var status serveStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return serveStatus{}, fmt.Errorf("decode Tailscale Serve status: %w", err)
	}
	return status, nil
}

func inspectTailscale(ctx context.Context, options Options) (bool, error) {
	status, err := readTailscaleStatus(ctx, options)
	if err != nil {
		return false, err
	}
	return status.panelRoute()
}

// Switching away from Tailscale removes only the exact WPX background root
// handler. Do not use `serve reset` or port-wide `off`: the operator may have
// other paths, ports, named services, or foreground sessions on the same node.
func removePanelTailscale(ctx context.Context, options Options) (bool, error) {
	if _, err := os.Stat(options.TailscalePath); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("inspect Tailscale executable: %w", err)
	}
	status, err := readTailscaleStatus(ctx, options)
	if err != nil {
		return false, err
	}
	for endpoint, web := range status.Web {
		if !strings.HasSuffix(endpoint, ":443") {
			continue
		}
		handler, exists := web.Handlers["/"]
		if exists && handler.Proxy == tailscaleTarget && handler.Path == "" && handler.Text == "" && handler.Redirect == "" {
			if err := options.Runner.Run(ctx, options.TailscalePath, "serve", "--bg", "--yes", "--https=443", "--set-path=/", "off"); err != nil {
				return false, fmt.Errorf("remove previous panel Tailscale Serve route: %w", err)
			}
			return true, nil
		}
	}
	return false, nil
}

func (status serveStatus) panelRoute() (bool, error) {
	for endpoint, allowed := range status.AllowFunnel {
		if allowed && strings.HasSuffix(endpoint, ":443") {
			return false, errors.New("Tailscale Funnel already exposes port 443 publicly; disable it before configuring private panel access")
		}
	}
	for _, foreground := range status.Foreground {
		if _, exists := foreground.TCP["443"]; exists {
			return false, errors.New("refuse to replace a foreground Tailscale listener on port 443")
		}
		for endpoint := range foreground.Web {
			if strings.HasSuffix(endpoint, ":443") {
				return false, errors.New("refuse to replace a foreground Tailscale route on port 443")
			}
		}
	}
	alreadyConfigured := false
	for endpoint, web := range status.Web {
		if !strings.HasSuffix(endpoint, ":443") {
			continue
		}
		for path, handler := range web.Handlers {
			if path != "/" || handler.Proxy != tailscaleTarget || handler.Path != "" || handler.Text != "" || handler.Redirect != "" {
				return false, errors.New("refuse to replace existing Tailscale Serve routes on port 443; choose an unused port or remove the conflicting route manually")
			}
			alreadyConfigured = true
		}
	}
	if listener, exists := status.TCP["443"]; exists && (!listener.HTTPS || !alreadyConfigured) {
		return false, errors.New("refuse to replace an existing Tailscale listener on port 443")
	}
	return alreadyConfigured, nil
}
