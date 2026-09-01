package dns

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

type Manager interface {
	Verify(context.Context, model.DNSProvider) error
	Apply(context.Context, model.DNSProvider, model.DNSRecord) (string, error)
	Delete(context.Context, model.DNSProvider, model.DNSRecord) error
}

func (c *Client) Delete(ctx context.Context, provider model.DNSProvider, record model.DNSRecord) error {
	if err := model.ValidateDNSProvider(provider); err != nil {
		return err
	}
	if err := model.ValidateDNSRecord(record, provider); err != nil {
		return err
	}
	switch provider.Kind {
	case model.DNSCloudflare:
		if record.RemoteID == "" {
			return errors.New("Cloudflare record identifier is unavailable")
		}
		endpoint := c.cloudflareBase() + "/zones/" + url.PathEscape(provider.ZoneID) + "/dns_records/" + url.PathEscape(record.RemoteID)
		request, _ := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
		request.Header.Set("Authorization", "Bearer "+provider.APIToken)
		return c.doCloudflare(request, nil)
	case model.DNSRoute53:
		return c.changeRoute53(ctx, provider, record, "DELETE")
	default:
		return errors.New("unsupported DNS provider")
	}
}

type Client struct {
	HTTP           *http.Client
	Now            func() time.Time
	CloudflareBase string
	Route53Base    string
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}, Now: time.Now}
}

func (c *Client) Verify(ctx context.Context, provider model.DNSProvider) error {
	if err := model.ValidateDNSProvider(provider); err != nil {
		return err
	}
	switch provider.Kind {
	case model.DNSCloudflare:
		endpoint := c.cloudflareBase() + "/zones/" + url.PathEscape(provider.ZoneID) + "/dns_records?per_page=1"
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		request.Header.Set("Authorization", "Bearer "+provider.APIToken)
		return c.doCloudflare(request, nil)
	case model.DNSRoute53:
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.route53Base()+"/2013-04-01/hostedzone/"+url.PathEscape(provider.ZoneID), nil)
		request.Header.Set("Content-Type", "application/xml")
		c.signRoute53(request, nil, provider)
		return c.doAWS(request)
	default:
		return errors.New("unsupported DNS provider")
	}
}

func (c *Client) Apply(ctx context.Context, provider model.DNSProvider, record model.DNSRecord) (string, error) {
	if err := model.ValidateDNSProvider(provider); err != nil {
		return "", err
	}
	if err := model.ValidateDNSRecord(record, provider); err != nil {
		return "", err
	}
	switch provider.Kind {
	case model.DNSCloudflare:
		return c.applyCloudflare(ctx, provider, record)
	case model.DNSRoute53:
		return c.applyRoute53(ctx, provider, record)
	default:
		return "", errors.New("unsupported DNS provider")
	}
}

func (c *Client) applyCloudflare(ctx context.Context, provider model.DNSProvider, record model.DNSRecord) (string, error) {
	body, _ := json.Marshal(map[string]any{"type": record.Type, "name": record.Name, "content": record.Value, "ttl": record.TTL, "proxied": record.Proxied, "comment": "Managed by WPX"})
	method := http.MethodPost
	endpoint := c.cloudflareBase() + "/zones/" + url.PathEscape(provider.ZoneID) + "/dns_records"
	if record.RemoteID != "" {
		method = http.MethodPut
		endpoint += "/" + url.PathEscape(record.RemoteID)
	}
	request, _ := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+provider.APIToken)
	request.Header.Set("Content-Type", "application/json")
	var response struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := c.doCloudflare(request, &response); err != nil {
		return "", err
	}
	if response.Result.ID == "" {
		return "", errors.New("Cloudflare did not return a record identifier")
	}
	return response.Result.ID, nil
}

func (c *Client) doCloudflare(request *http.Request, output any) error {
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("Cloudflare request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(body, &envelope) != nil || response.StatusCode/100 != 2 || !envelope.Success {
		return fmt.Errorf("Cloudflare API returned HTTP %d", response.StatusCode)
	}
	if output != nil && json.Unmarshal(body, output) != nil {
		return errors.New("Cloudflare returned an invalid response")
	}
	return nil
}

type route53Change struct {
	XMLName xml.Name `xml:"ChangeResourceRecordSetsRequest"`
	XMLNS   string   `xml:"xmlns,attr"`
	Batch   struct {
		Comment string `xml:"Comment"`
		Changes []struct {
			Action string `xml:"Action"`
			Set    struct {
				Name      string `xml:"Name"`
				Type      string `xml:"Type"`
				TTL       int    `xml:"TTL"`
				Resources []struct {
					Value string `xml:"Value"`
				} `xml:"ResourceRecords>ResourceRecord"`
			} `xml:"ResourceRecordSet"`
		} `xml:"Changes>Change"`
	} `xml:"ChangeBatch"`
}

func (c *Client) applyRoute53(ctx context.Context, provider model.DNSProvider, record model.DNSRecord) (string, error) {
	if err := c.changeRoute53(ctx, provider, record, "UPSERT"); err != nil {
		return "", err
	}
	return record.Name + "|" + record.Type, nil
}

func (c *Client) changeRoute53(ctx context.Context, provider model.DNSProvider, record model.DNSRecord, action string) error {
	var change route53Change
	change.XMLNS = "https://route53.amazonaws.com/doc/2013-04-01/"
	change.Batch.Comment = "Managed by WPX"
	change.Batch.Changes = make([]struct {
		Action string `xml:"Action"`
		Set    struct {
			Name      string `xml:"Name"`
			Type      string `xml:"Type"`
			TTL       int    `xml:"TTL"`
			Resources []struct {
				Value string `xml:"Value"`
			} `xml:"ResourceRecords>ResourceRecord"`
		} `xml:"ResourceRecordSet"`
	}, 1)
	entry := &change.Batch.Changes[0]
	entry.Action, entry.Set.Name, entry.Set.Type, entry.Set.TTL = action, record.Name, record.Type, record.TTL
	value := record.Value
	if record.Type == "TXT" {
		value = `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	entry.Set.Resources = append(entry.Set.Resources, struct {
		Value string `xml:"Value"`
	}{Value: value})
	body, err := xml.Marshal(change)
	if err != nil {
		return err
	}
	endpoint := c.route53Base() + "/2013-04-01/hostedzone/" + url.PathEscape(provider.ZoneID) + "/rrset"
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/xml")
	c.signRoute53(request, body, provider)
	if err := c.doAWS(request); err != nil {
		return err
	}
	return nil
}

func (c *Client) doAWS(request *http.Request) error {
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("Route 53 request: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("Route 53 API returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *Client) signRoute53(request *http.Request, payload []byte, provider model.DNSProvider) {
	now := c.now().UTC()
	amzDate, shortDate := now.Format("20060102T150405Z"), now.Format("20060102")
	payloadHash := sha256Hex(payload)
	request.Header.Set("X-Amz-Date", amzDate)
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if provider.SessionToken != "" {
		request.Header.Set("X-Amz-Security-Token", provider.SessionToken)
	}
	headerNames := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	canonicalHeaders := "content-type:" + strings.TrimSpace(request.Header.Get("Content-Type")) + "\n" +
		"host:" + request.URL.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	if provider.SessionToken != "" {
		headerNames = append(headerNames, "x-amz-security-token")
		canonicalHeaders += "x-amz-security-token:" + strings.TrimSpace(provider.SessionToken) + "\n"
	}
	signedHeaders := strings.Join(headerNames, ";")
	canonicalRequest := request.Method + "\n" + request.URL.EscapedPath() + "\n" + request.URL.RawQuery + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash
	scope := shortDate + "/us-east-1/route53/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))
	kDate := hmacSHA256([]byte("AWS4"+provider.SecretKey), shortDate)
	kRegion := hmacSHA256(kDate, "us-east-1")
	kService := hmacSHA256(kRegion, "route53")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+provider.AccessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return &http.Client{Timeout: 20 * time.Second}
	}
	return c.HTTP
}

func (c *Client) cloudflareBase() string {
	if c.CloudflareBase == "" {
		return "https://api.cloudflare.com/client/v4"
	}
	return strings.TrimRight(c.CloudflareBase, "/")
}

func (c *Client) route53Base() string {
	if c.Route53Base == "" {
		return "https://route53.amazonaws.com"
	}
	return strings.TrimRight(c.Route53Base, "/")
}

func (c *Client) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}
