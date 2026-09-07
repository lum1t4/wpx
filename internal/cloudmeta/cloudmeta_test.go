package cloudmeta

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string, headers map[string]string) *http.Response {
	h := make(http.Header)
	for key, value := range headers {
		h.Set(key, value)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func TestCacheDetectsOnceWithoutBlockingReaders(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	cache := &Cache{done: make(chan struct{}), detect: func(context.Context) Result {
		calls.Add(1)
		close(started)
		<-release
		return Result{Provider: Hetzner, PublicIP: "1.1.1.1"}
	}}
	cache.Start(context.Background())
	cache.Start(context.Background())
	<-started
	if _, ready := cache.Result(); ready {
		t.Fatal("result reported ready while detection is running")
	}
	close(release)
	<-cache.done
	if got, ready := cache.Result(); !ready || got.Provider != Hetzner || calls.Load() != 1 {
		t.Fatalf("cached result = %#v, ready %v, calls %d", got, ready, calls.Load())
	}
}

func TestAWSRequiresIMDSv2Token(t *testing.T) {
	var calls []string
	d := detectorWithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != metadataHost || r.URL.Scheme != "http" {
			t.Fatalf("request escaped fixed metadata origin: %s", r.URL)
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/latest/api/token":
			if r.Method != http.MethodPut || r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") != "60" {
				t.Fatalf("invalid token request: %#v", r)
			}
			return response(200, "token-value", nil), nil
		case "/latest/meta-data/public-ipv4":
			if r.Header.Get("X-aws-ec2-metadata-token") != "token-value" {
				t.Fatal("public IP request omitted IMDSv2 token")
			}
			return response(200, "8.8.8.8", nil), nil
		default:
			return response(404, "", nil), nil
		}
	}))
	got := d.detect(context.Background())
	if got != (Result{Provider: AWS, PublicIP: "8.8.8.8"}) {
		t.Fatalf("result = %#v", got)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestGoogleRequiresRequestAndResponseFlavor(t *testing.T) {
	d := detectorWithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip" {
			if r.Header.Get("Metadata-Flavor") != "Google" {
				t.Fatal("Google request omitted metadata header")
			}
			return response(200, "8.8.4.4", map[string]string{"Metadata-Flavor": "Google"}), nil
		}
		return response(404, "", nil), nil
	}))
	got := d.detect(context.Background())
	if got.Provider != GoogleCloud || got.PublicIP != "8.8.4.4" {
		t.Fatalf("result = %#v", got)
	}
}

func TestSupportedProviderPathsAndHeaders(t *testing.T) {
	tests := []struct {
		provider Provider
		path     string
		header   string
	}{
		{DigitalOcean, "/metadata/v1/interfaces/public/0/ipv4/address", ""},
		{Hetzner, "/hetzner/v1/metadata/public-ipv4", ""},
		{Vultr, "/v1.json", "cloudinit"},
	}
	for _, test := range tests {
		t.Run(string(test.provider), func(t *testing.T) {
			d := detectorWithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == test.path {
					if got := r.Header.Get("Metadata-Token"); got != test.header {
						t.Fatalf("metadata token = %q", got)
					}
					if test.provider == Vultr {
						return response(200, `{"interfaces":[{"ipv4":{"address":"1.1.1.1"}}]}`, nil), nil
					}
					return response(200, "1.1.1.1", nil), nil
				}
				return response(404, "", nil), nil
			}))
			got := d.detect(context.Background())
			if got.Provider != test.provider || got.PublicIP != "1.1.1.1" {
				t.Fatalf("result = %#v", got)
			}
		})
	}
}

func TestInvalidOversizedAndRedirectResponsesFallBack(t *testing.T) {
	for name, fixture := range map[string]struct {
		status int
		body   string
	}{
		"private":   {200, "10.0.0.1"},
		"malformed": {200, "not-an-ip"},
		"oversized": {200, strings.Repeat("1", requestLimit+1)},
		"redirect":  {302, "8.8.8.8"},
	} {
		t.Run(name, func(t *testing.T) {
			d := detectorWithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return response(fixture.status, fixture.body, nil), nil
			}))
			if got := d.detect(context.Background()); got != (Result{}) {
				t.Fatalf("result = %#v", got)
			}
		})
	}
}
