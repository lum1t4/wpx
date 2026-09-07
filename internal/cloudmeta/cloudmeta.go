// Package cloudmeta discovers the public IPv4 address exposed by supported
// instance metadata services. It never accepts a URL from configuration or a
// request and its production transport can dial only the IPv4 link-local
// metadata address.
package cloudmeta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type Provider string

const (
	AWS          Provider = "aws"
	DigitalOcean Provider = "digitalocean"
	GoogleCloud  Provider = "gce"
	Hetzner      Provider = "hetzner"
	Vultr        Provider = "vultr"
)

type Result struct {
	Provider Provider
	PublicIP string
}

// Cache runs metadata discovery at most once and makes reads non-blocking. A
// web request therefore never waits for unreachable metadata endpoints.
type Cache struct {
	once   sync.Once
	done   chan struct{}
	mu     sync.RWMutex
	result Result
	detect func(context.Context) Result
}

func NewCache() *Cache {
	return &Cache{done: make(chan struct{}), detect: Detect}
}

func (c *Cache) Start(ctx context.Context) {
	c.once.Do(func() {
		go func() {
			result := c.detect(ctx)
			c.mu.Lock()
			c.result = result
			c.mu.Unlock()
			close(c.done)
		}()
	})
}

func (c *Cache) Result() (Result, bool) {
	select {
	case <-c.done:
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.result, true
	default:
		return Result{}, false
	}
}

const (
	metadataHost = "169.254.169.254"
	metadataBase = "http://" + metadataHost
	requestLimit = 128
)

type detector struct{ client *http.Client }

// Detect is deliberately best-effort. A development machine, private-only VM,
// disabled metadata service, or provider outage simply produces an empty result.
func Detect(ctx context.Context) Result {
	dialer := &net.Dialer{Timeout: 250 * time.Millisecond}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != net.JoinHostPort(metadataHost, "80") {
				return nil, fmt.Errorf("cloud metadata dial refused: %s %s", network, address)
			}
			return dialer.DialContext(ctx, "tcp4", address)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	return detectorWithTransport(transport).detect(ctx)
}

func detectorWithTransport(roundTripper http.RoundTripper) detector {
	return detector{client: &http.Client{
		Transport: roundTripper,
		Timeout:   350 * time.Millisecond,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (d detector) detect(parent context.Context) Result {
	ctx, cancel := context.WithTimeout(parent, 1600*time.Millisecond)
	defer cancel()

	if token, ok := d.read(ctx, http.MethodPut, "/latest/api/token", map[string]string{
		"X-aws-ec2-metadata-token-ttl-seconds": "60",
	}, nil); ok && token != "" {
		if ip, found := d.readIP(ctx, "/latest/meta-data/public-ipv4", map[string]string{
			"X-aws-ec2-metadata-token": token,
		}, nil); found {
			return Result{Provider: AWS, PublicIP: ip}
		}
	}
	if ip, ok := d.readIP(ctx, "/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip", map[string]string{
		"Metadata-Flavor": "Google",
	}, func(response *http.Response) bool {
		return strings.EqualFold(response.Header.Get("Metadata-Flavor"), "Google")
	}); ok {
		return Result{Provider: GoogleCloud, PublicIP: ip}
	}
	if ip, ok := d.readIP(ctx, "/metadata/v1/interfaces/public/0/ipv4/address", nil, nil); ok {
		return Result{Provider: DigitalOcean, PublicIP: ip}
	}
	if ip, ok := d.readIP(ctx, "/hetzner/v1/metadata/public-ipv4", nil, nil); ok {
		return Result{Provider: Hetzner, PublicIP: ip}
	}
	if ip, ok := d.vultrIP(ctx); ok {
		return Result{Provider: Vultr, PublicIP: ip}
	}
	return Result{}
}

func (d detector) vultrIP(ctx context.Context) (string, bool) {
	body, ok := d.readRaw(ctx, http.MethodGet, "/v1.json", map[string]string{
		"Metadata-Token": "cloudinit",
	}, nil, 64<<10)
	if !ok {
		return "", false
	}
	var metadata struct {
		Interfaces []struct {
			IPv4 struct {
				Address string `json:"address"`
			} `json:"ipv4"`
		} `json:"interfaces"`
	}
	if json.Unmarshal(body, &metadata) != nil || len(metadata.Interfaces) == 0 {
		return "", false
	}
	return publicIPv4(metadata.Interfaces[0].IPv4.Address)
}

func (d detector) readIP(ctx context.Context, path string, headers map[string]string, validate func(*http.Response) bool) (string, bool) {
	value, ok := d.read(ctx, http.MethodGet, path, headers, validate)
	if !ok {
		return "", false
	}
	return publicIPv4(value)
}

func publicIPv4(value string) (string, bool) {
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || !address.Is4() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return "", false
	}
	return address.String(), true
}

func (d detector) read(ctx context.Context, method, path string, headers map[string]string, validate func(*http.Response) bool) (string, bool) {
	body, ok := d.readRaw(ctx, method, path, headers, validate, requestLimit)
	if !ok {
		return "", false
	}
	value := strings.TrimSpace(string(body))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	return value, true
}

func (d detector) readRaw(ctx context.Context, method, path string, headers map[string]string, validate func(*http.Response) bool, limit int64) ([]byte, bool) {
	request, err := http.NewRequestWithContext(ctx, method, metadataBase+path, nil)
	if err != nil {
		return nil, false
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := d.client.Do(request)
	if err != nil {
		return nil, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || (validate != nil && !validate(response)) {
		return nil, false
	}
	limited := io.LimitReader(response.Body, limit+1)
	body, err := io.ReadAll(limited)
	if err != nil || int64(len(body)) > limit {
		return nil, false
	}
	return body, true
}
