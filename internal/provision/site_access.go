package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

const (
	accessOwnershipMarker = "# Managed by WPX site access.\n"
	defaultCloudflareURL  = "https://api.cloudflare.com/client/v4/ips"
)

var bundledCloudflareRanges = cloudflareRanges{
	IPv4: []string{"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22"},
	IPv6: []string{"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32"},
}

type cloudflareRanges struct {
	IPv4      []string  `json:"ipv4_cidrs"`
	IPv6      []string  `json:"ipv6_cidrs"`
	ETag      string    `json:"etag,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
}

type cloudflareResponse struct {
	Success bool `json:"success"`
	Result  struct {
		IPv4 []string `json:"ipv4_cidrs"`
		IPv6 []string `json:"ipv6_cidrs"`
		ETag string   `json:"etag"`
	} `json:"result"`
}

func (h *Host) accessRoot() string {
	return h.AccessRoot
}

func (h *Host) validateAccessRoot() error {
	root := h.accessRoot()
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return errors.New("site access root must be an absolute, clean, non-root path")
	}
	return nil
}

// EnsureAccessDefaults creates the unconditional include target without
// changing staging authentication. Callers already holding h.mu may use it.
func (h *Host) EnsureAccessDefaults(_ context.Context, site model.Site) error {
	if err := model.ValidateSiteID(site.ID); err != nil {
		return err
	}
	if err := h.validateAccessRoot(); err != nil {
		return err
	}
	name := site.ID + ".conf"
	if _, existed, err := secureManagedAccessRead(h.accessRoot(), name, accessOwnershipMarker); err != nil {
		return err
	} else if existed {
		return nil
	}
	return secureAccessWrite(h.accessRoot(), name, []byte(accessOwnershipMarker), 0644)
}

// InjectSiteAccessNginx adds the managed include and makes HTTP-01 challenges
// intentionally public. Cloudflare-only sites should use DNS-01 for routine
// issuance, but the exception preserves recovery through HTTP-01.
func (h *Host) InjectSiteAccessNginx(site model.Site, configuration string) (string, error) {
	if err := model.ValidateSiteID(site.ID); err != nil {
		return "", err
	}
	if err := h.validateAccessRoot(); err != nil {
		return "", err
	}
	needle := "    server_name "
	lineEnd := strings.Index(configuration, "\n")
	serverName := strings.Index(configuration, needle)
	if serverName < 0 || (lineEnd >= 0 && serverName < lineEnd) {
		return "", errors.New("managed Nginx server_name is missing")
	}
	serverNameEnd := strings.Index(configuration[serverName:], "\n")
	if serverNameEnd < 0 {
		return "", errors.New("managed Nginx server_name is incomplete")
	}
	serverNameEnd += serverName
	include := "    include " + filepath.Join(h.accessRoot(), site.ID+".conf") + ";"
	if !strings.Contains(configuration, include) {
		configuration = configuration[:serverNameEnd+1] + include + "\n" + configuration[serverNameEnd+1:]
	}
	challenge := "location ^~ /.well-known/acme-challenge/ {"
	if strings.Contains(configuration, challenge) {
		configuration = strings.ReplaceAll(configuration, challenge+" root ", challenge+" auth_basic off; allow all; root ")
	} else {
		publicDir := filepath.Join(h.SiteRoot, site.ID, "public")
		location := "    " + challenge + " auth_basic off; allow all; root " + publicDir + "; }\n"
		insert := strings.Index(configuration, "    location ")
		if insert < 0 {
			return "", errors.New("managed Nginx location is missing")
		}
		configuration = configuration[:insert] + location + configuration[insert:]
	}
	return configuration, nil
}

func (h *Host) ApplySiteAccess(ctx context.Context, site model.Site, settings model.SiteAccessSettings) error {
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if site.Status != "active" || settings.SiteID != site.ID {
		return errors.New("access settings require the matching active site")
	}
	if err := model.ValidateSiteAccessSettings(settings); err != nil {
		return err
	}
	if err := h.validate(); err != nil {
		return err
	}
	if err := h.validateAccessRoot(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Older WPX vhosts predate the access include. Install it into both active
	// HTTP and TLS configurations during the first settings apply, and keep the
	// previous complete files so validation or reload failure can restore them.
	type vhostState struct {
		path     string
		content  []byte
		desired  []byte
		existed  bool
		modified bool
	}
	vhosts := make([]vhostState, 0, 2)
	for _, name := range []string{"wpx-" + site.ID + ".conf", "wpx-" + site.ID + "-tls.conf"} {
		path := filepath.Join(h.NginxAvailable, name)
		content, existed, err := secureManagedAccessRead(h.NginxAvailable, name, ownershipMarker)
		if err != nil {
			return fmt.Errorf("inspect site Nginx configuration: %w", err)
		}
		if !existed {
			if len(vhosts) == 0 {
				return errors.New("active site Nginx configuration is missing")
			}
			continue
		}
		decorated, err := h.InjectSiteAccessNginx(site, string(content))
		if err != nil {
			return fmt.Errorf("prepare site Nginx access include: %w", err)
		}
		state := vhostState{path: path, content: content, desired: []byte(decorated), existed: true, modified: decorated != string(content)}
		vhosts = append(vhosts, state)
	}

	ranges := cloudflareRanges{}
	if settings.CloudflareOnly {
		ranges = h.cloudflareRanges(ctx)
	}
	include := accessOwnershipMarker
	passwordPath := filepath.Join(h.accessRoot(), site.ID+".htpasswd")
	if settings.BasicAuthEnabled {
		include += "auth_basic \"Restricted\";\n" +
			"auth_basic_user_file " + passwordPath + ";\n"
	}
	if settings.CloudflareOnly {
		for _, cidr := range append(append([]string(nil), ranges.IPv4...), ranges.IPv6...) {
			include += "allow " + cidr + ";\n"
		}
		include += "deny all;\n"
	}

	includeName := site.ID + ".conf"
	previousInclude, includeExisted, err := secureManagedAccessRead(h.accessRoot(), includeName, accessOwnershipMarker)
	if err != nil {
		return fmt.Errorf("inspect site access include: %w", err)
	}
	passwordName := site.ID + ".htpasswd"
	previousPassword, passwordExisted, err := secureManagedAccessRead(h.accessRoot(), passwordName, accessOwnershipMarker)
	if err != nil {
		return fmt.Errorf("inspect site access password file: %w", err)
	}
	restore := func() error {
		var restoreErrors []error
		if err := restoreManagedAccessFile(h.accessRoot(), includeName, previousInclude, includeExisted, 0644); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
		if err := restoreManagedAccessFile(h.accessRoot(), passwordName, previousPassword, passwordExisted, 0640); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
		if passwordExisted {
			if gid, lookupErr := nginxAccessGID(); lookupErr == nil {
				if err := secureAccessChown(h.accessRoot(), passwordName, 0, gid); err != nil {
					restoreErrors = append(restoreErrors, err)
				}
			} else {
				restoreErrors = append(restoreErrors, lookupErr)
			}
		}
		for _, prior := range vhosts {
			if prior.modified {
				if err := restoreManagedAccessFile(h.NginxAvailable, filepath.Base(prior.path), prior.content, prior.existed, 0644); err != nil {
					restoreErrors = append(restoreErrors, err)
				}
			}
		}
		return errors.Join(restoreErrors...)
	}
	for _, candidate := range vhosts {
		if candidate.modified {
			if err := secureAccessWrite(h.NginxAvailable, filepath.Base(candidate.path), candidate.desired, 0644); err != nil {
				return accessRollbackError(fmt.Errorf("install site Nginx access include: %w", err), restore())
			}
		}
	}
	if settings.BasicAuthEnabled {
		// Ubuntu's crypt implementation and Nginx use the $2y$ bcrypt marker;
		// its payload is otherwise identical to Go's $2a$ encoding.
		encodedHash := strings.Replace(settings.PasswordHash, "$2a$", "$2y$", 1)
		password := accessOwnershipMarker + settings.Username + ":" + encodedHash + "\n"
		if err := secureAccessWrite(h.accessRoot(), passwordName, []byte(password), 0640); err != nil {
			return accessRollbackError(fmt.Errorf("write site access password file: %w", err), restore())
		}
		gid, err := nginxAccessGID()
		if err != nil {
			return accessRollbackError(err, restore())
		}
		if err := secureAccessChown(h.accessRoot(), passwordName, 0, gid); err != nil {
			return accessRollbackError(fmt.Errorf("grant Nginx access to site password file: %w", err), restore())
		}
	}
	if err := secureAccessWrite(h.accessRoot(), includeName, []byte(include), 0644); err != nil {
		return accessRollbackError(fmt.Errorf("write site access include: %w", err), restore())
	}
	if err := h.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		return accessRollbackError(fmt.Errorf("validate Nginx access configuration: %w", err), restore())
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		restoreErr := restore()
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		recoveryErr := h.Runner.Run(recoveryCtx, "/usr/sbin/nginx", "-t")
		if recoveryErr == nil {
			recoveryErr = h.Runner.Run(recoveryCtx, "/usr/bin/systemctl", "reload", "nginx.service")
		}
		if recoveryErr != nil {
			return accessRollbackError(fmt.Errorf("reload Nginx access configuration: %w; rollback reload failed: %v", err, recoveryErr), restoreErr)
		}
		return accessRollbackError(fmt.Errorf("reload Nginx access configuration: %w; previous access restored", err), restoreErr)
	}
	return nil
}

func restoreManagedAccessFile(root, name string, content []byte, existed bool, mode os.FileMode) error {
	if existed {
		return secureAccessWrite(root, name, content, mode)
	}
	return secureAccessRemove(root, name)
}

func accessRollbackError(cause, rollbackErr error) error {
	if rollbackErr == nil {
		return cause
	}
	return fmt.Errorf("%w; rollback failed: %v", cause, rollbackErr)
}

func nginxAccessGID() (int, error) {
	group, err := user.LookupGroup("www-data")
	if err != nil {
		return 0, fmt.Errorf("look up Nginx group: %w", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return 0, fmt.Errorf("decode Nginx group: %w", err)
	}
	return gid, nil
}

func (h *Host) cloudflareRanges(ctx context.Context) cloudflareRanges {
	cacheRoot, cacheName := filepath.Dir(h.accessRoot()), "cloudflare-ranges.json"
	cached, cachedOK := readCloudflareCache(cacheRoot, cacheName)
	ttl := h.CloudflareRangesTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if cachedOK && time.Since(cached.FetchedAt) < ttl {
		return cached
	}
	endpoint := h.CloudflareRangesURL
	if endpoint == "" {
		endpoint = defaultCloudflareURL
	}
	client := h.CloudflareHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err == nil {
		var response *http.Response
		response, err = client.Do(request)
		if err == nil {
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("Cloudflare ranges returned HTTP %d", response.StatusCode)
			} else {
				var payload cloudflareResponse
				var body []byte
				body, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
				if err == nil && len(body) > 1<<20 {
					err = errors.New("Cloudflare ranges response is too large")
				}
				if err == nil {
					err = json.Unmarshal(body, &payload)
				}
				fresh := cloudflareRanges{IPv4: payload.Result.IPv4, IPv6: payload.Result.IPv6, ETag: payload.Result.ETag, FetchedAt: time.Now().UTC()}
				if err == nil && payload.Success && validateCloudflareRanges(fresh) == nil {
					if encoded, encodeErr := json.Marshal(fresh); encodeErr == nil {
						_ = secureAccessWrite(cacheRoot, cacheName, append(encoded, '\n'), 0644)
					}
					return fresh
				}
			}
		}
	}
	if cachedOK {
		return cached
	}
	// The bundled official list is deliberately non-empty. A refresh failure can
	// reduce freshness, but can never turn the origin restriction into allow-all.
	return bundledCloudflareRanges
}

func readCloudflareCache(root, name string) (cloudflareRanges, bool) {
	content, existed, err := secureManagedAccessRead(root, name, "{")
	if err != nil || !existed {
		return cloudflareRanges{}, false
	}
	var ranges cloudflareRanges
	if json.Unmarshal(content, &ranges) != nil || ranges.FetchedAt.IsZero() || ranges.FetchedAt.After(time.Now().Add(5*time.Minute)) || validateCloudflareRanges(ranges) != nil {
		return cloudflareRanges{}, false
	}
	return ranges, true
}

func validateCloudflareRanges(ranges cloudflareRanges) error {
	if len(ranges.IPv4) == 0 || len(ranges.IPv6) == 0 || len(ranges.IPv4)+len(ranges.IPv6) > 128 {
		return errors.New("Cloudflare response must contain bounded IPv4 and IPv6 ranges")
	}
	seen := make(map[string]struct{})
	for _, family := range []struct {
		values []string
		bits   int
	}{{ranges.IPv4, 32}, {ranges.IPv6, 128}} {
		for _, value := range family.values {
			ip, network, err := net.ParseCIDR(value)
			if err != nil || network.String() != value || (ip.To4() != nil) != (family.bits == 32) {
				return errors.New("Cloudflare response contains an invalid canonical CIDR")
			}
			if _, ok := seen[value]; ok {
				return errors.New("Cloudflare response contains duplicate CIDRs")
			}
			seen[value] = struct{}{}
		}
	}
	sort.Strings(ranges.IPv4)
	sort.Strings(ranges.IPv6)
	return nil
}
