package provision

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

const (
	securityLogBytes = 512 << 10
	securityLogLines = 2000
	securityMarker   = "# Managed by WPX security defense. Manual changes will be replaced.\n"
)

type securityPaths struct {
	nginxGlobal     string
	nginxSites      string
	fail2banJails   string
	fail2banFilters string
}

func hostSecurityPaths(h *Host) securityPaths {
	return securityPaths{
		nginxGlobal:     h.SecurityNginxGlobal,
		nginxSites:      h.SecurityNginxRoot,
		fail2banJails:   h.SecurityFail2banJails,
		fail2banFilters: h.SecurityFail2banFilters,
	}
}

// EnsureSecurityDefaults creates the harmless include required by generated
// vhosts. It does not enable a jail or a request limit; protection remains an
// explicit site setting.
func (h *Host) EnsureSecurityDefaults(site model.Site) error {
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if site.Kind != model.WordPress {
		return nil
	}
	return ensureSecurityDefaults(hostSecurityPaths(h), site)
}

func ensureSecurityDefaults(paths securityPaths, site model.Site) error {
	if err := validateSecurityPaths(paths); err != nil {
		return err
	}
	if err := ensureSecurityDirectory(paths.nginxSites); err != nil {
		return err
	}
	if err := ensureSecurityDirectory(filepath.Dir(paths.nginxGlobal)); err != nil {
		return err
	}
	_, globalExists, err := securityManagedState(paths.nginxGlobal)
	if err != nil {
		return err
	}
	if !globalExists {
		if err := secureAtomicWrite(paths.nginxGlobal, []byte(renderSecurityZones()), 0644); err != nil {
			return err
		}
	}
	path := filepath.Join(paths.nginxSites, site.ID+".conf")
	_, siteExists, err := securityManagedState(path)
	if err != nil {
		return err
	}
	if siteExists {
		return nil
	}
	return secureAtomicWrite(path, []byte(securityMarker), 0644)
}

// InjectSiteSecurityNginx adds the always-present per-site include to a
// generated WordPress server block. The include remains harmless until an
// operator enables at least one protection.
func (h *Host) InjectSiteSecurityNginx(site model.Site, configuration string) (string, error) {
	if site.Kind != model.WordPress {
		return configuration, nil
	}
	if err := h.EnsureSecurityDefaults(site); err != nil {
		return "", err
	}
	needle := "    server_name "
	start := strings.Index(configuration, needle)
	if start < 0 || !strings.HasPrefix(configuration, ownershipMarker) {
		return "", errors.New("generated Nginx configuration cannot accept security include")
	}
	end := strings.IndexByte(configuration[start:], '\n')
	if end < 0 {
		return "", errors.New("generated Nginx server_name is incomplete")
	}
	end += start + 1
	include := "    include " + filepath.Join(h.SecurityNginxRoot, site.ID+".conf") + ";\n"
	if strings.Contains(configuration, include) {
		return configuration, nil
	}
	return configuration[:end] + include + configuration[end:], nil
}

func (h *Host) ApplySecurity(ctx context.Context, site model.Site, settings model.SecuritySettings) error {
	if err := model.ValidateSecuritySettings(site, settings); err != nil {
		return err
	}
	if h.Runner == nil {
		return errors.New("security runner is unavailable")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return applySecurity(ctx, h.Runner, hostSecurityPaths(h), site, settings)
}

func (h *Host) SecurityAvailable() bool {
	for _, path := range []string{"/usr/bin/fail2ban-client", "/usr/sbin/nft"} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return false
		}
	}
	return true
}

// InstallSecurity is invoked only by the durable worker. Arguments are fixed;
// no browser value can choose a package or executable.
func (h *Host) InstallSecurity(ctx context.Context) error {
	if h.Runner == nil {
		return errors.New("security runner is unavailable")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return installSecurity(ctx, h.Runner, h.SecurityAvailable())
}

func installSecurity(ctx context.Context, runner Runner, available bool) error {
	if available {
		return runner.Run(ctx, "/usr/bin/systemctl", "enable", "--now", "fail2ban.service")
	}
	if err := runner.Run(ctx, "/usr/bin/apt-get", "update"); err != nil {
		return fmt.Errorf("update package index: %w", err)
	}
	if err := runner.Run(ctx, "/usr/bin/apt-get", "install", "-y", "--no-install-recommends", "fail2ban", "nftables"); err != nil {
		return fmt.Errorf("install security packages: %w", err)
	}
	if err := runner.Run(ctx, "/usr/bin/systemctl", "enable", "--now", "fail2ban.service"); err != nil {
		return fmt.Errorf("start Fail2ban: %w", err)
	}
	return nil
}

type securityWrite struct {
	path     string
	content  []byte
	previous []byte
	existed  bool
}

func applySecurity(ctx context.Context, runner Runner, paths securityPaths, site model.Site, settings model.SecuritySettings) error {
	if err := validateSecurityPaths(paths); err != nil {
		return err
	}
	for _, directory := range []string{filepath.Dir(paths.nginxGlobal), paths.nginxSites, paths.fail2banJails, paths.fail2banFilters} {
		if err := ensureSecurityDirectory(directory); err != nil {
			return err
		}
	}
	writes := []securityWrite{
		{path: paths.nginxGlobal, content: []byte(renderSecurityZones())},
		{path: filepath.Join(paths.nginxSites, site.ID+".conf"), content: []byte(renderSiteSecurity(settings))},
		{path: filepath.Join(paths.fail2banFilters, "wpx-wordpress-login.conf"), content: []byte(renderSecurityFilter("login"))},
		{path: filepath.Join(paths.fail2banFilters, "wpx-wordpress-xmlrpc.conf"), content: []byte(renderSecurityFilter("xmlrpc"))},
		{path: filepath.Join(paths.fail2banFilters, "wpx-sensitive-path.conf"), content: []byte(renderSecurityFilter("sensitive"))},
		{path: filepath.Join(paths.fail2banFilters, "wpx-404-burst.conf"), content: []byte(renderSecurityFilter("404"))},
		{path: filepath.Join(paths.fail2banJails, "wpx-"+site.ID+".local"), content: []byte(renderSecurityJails(site, settings))},
	}
	for i := range writes {
		var err error
		writes[i].previous, writes[i].existed, err = securityManagedState(writes[i].path)
		if err != nil {
			return err
		}
	}
	rollback := func() error {
		var rollbackErr error
		for i := len(writes) - 1; i >= 0; i-- {
			if writes[i].existed {
				rollbackErr = errors.Join(rollbackErr, secureAtomicWrite(writes[i].path, writes[i].previous, 0644))
			} else {
				rollbackErr = errors.Join(rollbackErr, secureRemove(writes[i].path))
			}
		}
		return rollbackErr
	}
	for _, write := range writes {
		if err := secureAtomicWrite(write.path, write.content, 0644); err != nil {
			return errors.Join(err, rollback())
		}
	}
	if err := runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		return errors.Join(fmt.Errorf("validate Nginx security configuration: %w", err), rollback())
	}
	if err := runner.Run(ctx, "/usr/bin/fail2ban-client", "-t"); err != nil {
		return errors.Join(fmt.Errorf("validate Fail2ban security configuration: %w", err), rollback())
	}
	if err := runner.Run(ctx, "/usr/bin/systemctl", "reload", "fail2ban.service"); err != nil {
		rollbackErr := rollback()
		rollbackErr = errors.Join(rollbackErr, runner.Run(ctx, "/usr/bin/systemctl", "reload", "fail2ban.service"))
		return errors.Join(fmt.Errorf("reload Fail2ban security configuration: %w", err), rollbackErr)
	}
	if err := runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		rollbackErr := rollback()
		rollbackErr = errors.Join(rollbackErr, runner.Run(ctx, "/usr/bin/systemctl", "reload", "fail2ban.service"))
		rollbackErr = errors.Join(rollbackErr, runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"))
		return errors.Join(fmt.Errorf("reload Nginx security configuration: %w", err), rollbackErr)
	}
	return nil
}

func validateSecurityPaths(paths securityPaths) error {
	for name, path := range map[string]string{
		"Nginx global security file": paths.nginxGlobal,
		"Nginx site security root":   paths.nginxSites,
		"Fail2ban jail root":         paths.fail2banJails,
		"Fail2ban filter root":       paths.fail2banFilters,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return fmt.Errorf("%s must be an absolute, clean, non-root path", name)
		}
	}
	return nil
}

func renderSecurityZones() string {
	return securityMarker +
		"map $uri $wpx_login_client { default \"\"; ~^/wp-login\\.php$ \"$binary_remote_addr:$server_name\"; }\n" +
		"map $uri $wpx_xmlrpc_client { default \"\"; ~^/xmlrpc\\.php$ \"$binary_remote_addr:$server_name\"; }\n" +
		"map $uri $wpx_sensitive_path { default 0; ~*^/(?:\\.env(?:$|/)|\\.git(?:$|/)) 1; }\n" +
		"limit_req_zone $wpx_login_client zone=wpx_login:10m rate=5r/m;\n" +
		"limit_req_zone $wpx_xmlrpc_client zone=wpx_xmlrpc:10m rate=10r/m;\n"
}

func renderSiteSecurity(settings model.SecuritySettings) string {
	var content strings.Builder
	content.WriteString(securityMarker)
	if !settings.Enabled {
		return content.String()
	}
	if settings.LoginProtection {
		content.WriteString("limit_req zone=wpx_login burst=5 nodelay;\n")
	}
	if settings.XMLRPCProtection {
		content.WriteString("limit_req zone=wpx_xmlrpc burst=10 nodelay;\n")
	}
	if settings.SensitivePathProtection {
		content.WriteString("if ($wpx_sensitive_path) { return 404; }\n")
	}
	content.WriteString("limit_req_status 429;\n")
	return content.String()
}

func renderSecurityFilter(kind string) string {
	expression := map[string]string{
		"login":     `^<HOST> \S+ \S+ .*"(?:GET|POST|HEAD) /wp-login\.php(?:[? ]).*" \d{3} `,
		"xmlrpc":    `^<HOST> \S+ \S+ .*"(?:GET|POST|HEAD) /xmlrpc\.php(?:[? ]).*" \d{3} `,
		"sensitive": `^<HOST> \S+ \S+ .*"(?:GET|POST|HEAD) /(?:(?:\.env|\.git)(?:[/? ]|%%2[fF])).*" \d{3} `,
		"404":       `^<HOST> \S+ \S+ .*".*" 404 `,
	}[kind]
	return securityMarker + "[Definition]\nfailregex = " + expression + "\nignoreregex =\n"
}

func renderSecurityJails(site model.Site, settings model.SecuritySettings) string {
	logPath := "/var/log/nginx/wpx-" + site.ID + "-access.log"
	var content strings.Builder
	content.WriteString(securityMarker)
	content.WriteString("[DEFAULT]\nbackend = polling\nusedns = no\nbanaction = nftables[type=multiport]\nport = http,https\nprotocol = tcp\nbantime = 10m\nbantime.increment = true\nbantime.factor = 2\nbantime.maxtime = 1w\n\n")
	jails := []struct {
		name, filter string
		enabled      bool
		retry, find  string
	}{
		{"login", "wpx-wordpress-login", settings.Enabled && settings.LoginProtection, "5", "10m"},
		{"xmlrpc", "wpx-wordpress-xmlrpc", settings.Enabled && settings.XMLRPCProtection, "8", "10m"},
		{"sensitive", "wpx-sensitive-path", settings.Enabled && settings.SensitivePathProtection, "2", "10m"},
		{"404", "wpx-404-burst", settings.Enabled && settings.Burst404Protection, "30", "5m"},
	}
	for _, jail := range jails {
		fmt.Fprintf(&content, "[wpx-%s-%s]\nenabled = %t\nfilter = %s\nlogpath = %s\nmaxretry = %s\nfindtime = %s\n\n", site.ID, jail.name, jail.enabled, jail.filter, logPath, jail.retry, jail.find)
	}
	return content.String()
}

func (h *Host) InspectSecurity(ctx context.Context, site model.Site) (broker.SecurityReport, error) {
	if err := model.ValidateSite(site); err != nil || site.Kind != model.WordPress {
		return broker.SecurityReport{}, errors.New("security inspection requires a valid WordPress site")
	}
	if !filepath.IsAbs(h.NginxLogRoot) || filepath.Clean(h.NginxLogRoot) != h.NginxLogRoot || h.NginxLogRoot == "/" {
		return broker.SecurityReport{}, errors.New("invalid Nginx log root")
	}
	path := filepath.Join(h.NginxLogRoot, "wpx-"+site.ID+"-access.log")
	return inspectSecurityLog(ctx, path)
}

type observedSource struct {
	broker.SecuritySource
	last time.Time
}

func inspectSecurityLog(ctx context.Context, path string) (broker.SecurityReport, error) {
	lines, truncated, err := boundedSecurityLines(ctx, path)
	if err != nil {
		return broker.SecurityReport{}, err
	}
	report := broker.SecurityReport{Risk: "low", LinesRead: len(lines), Truncated: truncated}
	sources := make(map[netip.Addr]*observedSource)
	for _, line := range lines {
		address, at, target, status, ok := parseNginxAccess(line)
		if !ok {
			report.MalformedLines++
			continue
		}
		source := sources[address]
		if source == nil {
			source = &observedSource{SecuritySource: broker.SecuritySource{IP: address.String(), Risk: "low"}}
			sources[address] = source
		}
		source.Requests++
		if at.After(source.last) {
			source.last = at
		}
		path := strings.ToLower(target.Path)
		switch {
		case path == "/wp-login.php":
			source.LoginAttempts++
			source.Score++
		case path == "/xmlrpc.php":
			source.XMLRPCAttempts++
			source.Score++
		}
		if sensitiveRequestPath(path) {
			source.SensitiveScans++
			source.Score += 5
		}
		if status == 404 {
			source.NotFound++
			source.Score++
		}
	}
	for _, source := range sources {
		if source.LoginAttempts == 0 && source.XMLRPCAttempts == 0 && source.SensitiveScans == 0 && source.NotFound < 4 {
			continue
		}
		switch {
		case source.Score >= 10 || source.SensitiveScans >= 2:
			source.Risk = "high"
		case source.Score >= 4 || source.SensitiveScans > 0:
			source.Risk = "medium"
		default:
			source.Risk = "low"
		}
		if source.LoginAttempts > 0 {
			source.Reasons = append(source.Reasons, "WordPress login attempts")
		}
		if source.XMLRPCAttempts > 0 {
			source.Reasons = append(source.Reasons, "XML-RPC requests")
		}
		if source.SensitiveScans > 0 {
			source.Reasons = append(source.Reasons, "Sensitive path scans")
		}
		if source.NotFound >= 4 {
			source.Reasons = append(source.Reasons, "404 burst")
		}
		source.LastSeen = source.last.UTC().Format(time.RFC3339)
		if source.Risk == "high" {
			report.Risk = "high"
		} else if source.Risk == "medium" && report.Risk == "low" {
			report.Risk = "medium"
		}
		report.Sources = append(report.Sources, source.SecuritySource)
	}
	sort.Slice(report.Sources, func(i, j int) bool {
		if report.Sources[i].Score == report.Sources[j].Score {
			return report.Sources[i].IP < report.Sources[j].IP
		}
		return report.Sources[i].Score > report.Sources[j].Score
	})
	if len(report.Sources) > 50 {
		report.Sources = report.Sources[:50]
		report.Truncated = true
	}
	return report, nil
}

func boundedSecurityLines(ctx context.Context, path string) ([]string, bool, error) {
	pathInfo, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("site access log is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) {
		return nil, false, errors.New("site access log is not a regular file")
	}
	start := info.Size() - securityLogBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, false, err
	}
	reader := bufio.NewReader(io.LimitReader(file, securityLogBytes))
	if start > 0 {
		_, _ = reader.ReadString('\n')
	}
	lines := make([]string, 0, securityLogLines)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, start > 0, ctx.Err()
		default:
		}
		if len(lines) == securityLogLines {
			copy(lines, lines[1:])
			lines[len(lines)-1] = scanner.Text()
		} else {
			lines = append(lines, scanner.Text())
		}
	}
	return lines, start > 0 || len(lines) == securityLogLines, scanner.Err()
}

func parseNginxAccess(line string) (netip.Addr, time.Time, *url.URL, int, bool) {
	firstSpace := strings.IndexByte(line, ' ')
	if firstSpace <= 0 {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	address, err := netip.ParseAddr(line[:firstSpace])
	if err != nil {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	openTime, closeTime := strings.IndexByte(line, '['), strings.IndexByte(line, ']')
	if openTime < 0 || closeTime <= openTime {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	at, err := time.Parse("02/Jan/2006:15:04:05 -0700", line[openTime+1:closeTime])
	if err != nil {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	requestStart := strings.IndexByte(line[closeTime+1:], '"')
	if requestStart < 0 {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	requestStart += closeTime + 2
	requestEndRelative := strings.IndexByte(line[requestStart:], '"')
	if requestEndRelative < 0 {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	requestEnd := requestStart + requestEndRelative
	parts := strings.Fields(line[requestStart:requestEnd])
	if len(parts) != 3 || (parts[0] != "GET" && parts[0] != "POST" && parts[0] != "HEAD") || !strings.HasPrefix(parts[2], "HTTP/") || !strings.HasPrefix(parts[1], "/") {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	target, err := url.ParseRequestURI(parts[1])
	if err != nil || target.IsAbs() {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	statusFields := strings.Fields(line[requestEnd+1:])
	if len(statusFields) == 0 {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	status, err := strconv.Atoi(statusFields[0])
	if err != nil || status < 100 || status > 599 {
		return netip.Addr{}, time.Time{}, nil, 0, false
	}
	return address.Unmap(), at, target, status, true
}

func sensitiveRequestPath(path string) bool {
	return path == "/.env" || strings.HasPrefix(path, "/.env/") || path == "/.git" || strings.HasPrefix(path, "/.git/")
}
