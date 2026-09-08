package web

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxRequestLogLines      = 200
	maxRequestPathFilterLen = 256
)

var nginxCombinedLogPattern = regexp.MustCompile(`^([^ ]+) [^ ]+ [^ ]+ \[([^]]+)\] "([!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+) ([^" ]+) HTTP/[0-9.]+" ([1-5][0-9]{2}) ([0-9]+|-)(?: |$)`)

type RequestLog struct {
	Timestamp string
	IP        string
	Method    string
	Path      string
	Status    int
	Bytes     int64
	HasBytes  bool
}

type RequestLogFilters struct {
	IP     string
	Status string
	Method string
	Path   string
}

type RequestLogView struct {
	Rows         []RequestLog
	Filters      RequestLogFilters
	ParsedCount  int
	Skipped      int
	HasFilters   bool
	Error        string
	CurrentQuery string
	AllQuery     string
	ErrorsQuery  string
}

func requestLogView(r *http.Request, lines []string) (RequestLogView, error) {
	filters, err := parseRequestLogFilters(r)
	view := RequestLogView{
		Filters:      filters,
		HasFilters:   filters.IP != "" || filters.Status != "" || filters.Method != "" || filters.Path != "",
		CurrentQuery: requestLogQuery(filters, filters.Status),
		AllQuery:     requestLogQuery(filters, ""),
		ErrorsQuery:  requestLogQuery(filters, "errors"),
	}
	if err != nil {
		view.Error = err.Error()
		return view, err
	}
	if len(lines) > maxRequestLogLines {
		lines = lines[len(lines)-maxRequestLogLines:]
	}
	parsed := make([]RequestLog, 0, len(lines))
	for _, line := range lines {
		entry, ok := parseNginxAccessLog(line)
		if !ok {
			view.Skipped++
			continue
		}
		view.ParsedCount++
		if matchesRequestLog(entry, filters) {
			parsed = append(parsed, entry)
		}
	}
	// tailLines returns oldest first. Logs are most useful with the newest
	// matching request at the top, while keeping the original bounded read.
	view.Rows = make([]RequestLog, len(parsed))
	for i := range parsed {
		view.Rows[len(parsed)-1-i] = parsed[i]
	}
	return view, nil
}

func requestLogQuery(filters RequestLogFilters, status string) string {
	query := url.Values{}
	if filters.IP != "" {
		query.Set("ip", filters.IP)
	}
	if status != "" {
		query.Set("status", status)
	}
	if filters.Method != "" {
		query.Set("method", filters.Method)
	}
	if filters.Path != "" {
		query.Set("path", filters.Path)
	}
	if encoded := query.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}

func parseRequestLogFilters(r *http.Request) (RequestLogFilters, error) {
	filters := RequestLogFilters{
		IP:     strings.TrimSpace(r.URL.Query().Get("ip")),
		Status: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))),
		Method: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("method"))),
		Path:   strings.TrimSpace(r.URL.Query().Get("path")),
	}
	if filters.IP != "" {
		ip := net.ParseIP(filters.IP)
		if ip == nil {
			return filters, errors.New("IP filter must be one exact IPv4 or IPv6 address")
		}
		filters.IP = ip.String()
	}
	if filters.Status != "" {
		valid := filters.Status == "errors"
		if len(filters.Status) == 3 && filters.Status[1:] == "xx" && filters.Status[0] >= '1' && filters.Status[0] <= '5' {
			valid = true
		}
		if code, err := strconv.Atoi(filters.Status); err == nil && code >= 100 && code <= 599 {
			valid = true
		}
		if !valid {
			return filters, errors.New("status filter must be an HTTP status, a family such as 4xx, or Errors")
		}
	}
	if filters.Method != "" && !validHTTPMethod(filters.Method) {
		return filters, errors.New("method filter is invalid")
	}
	if len(filters.Path) > maxRequestPathFilterLen || strings.ContainsAny(filters.Path, "\r\n\x00") {
		return filters, errors.New("path filter must be at most 256 characters on one line")
	}
	return filters, nil
}

func validHTTPMethod(method string) bool {
	if len(method) == 0 || len(method) > 32 {
		return false
	}
	for _, character := range method {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", character) {
			continue
		}
		return false
	}
	return true
}

func parseNginxAccessLog(line string) (RequestLog, bool) {
	if len(line) == 0 || len(line) > 64<<10 {
		return RequestLog{}, false
	}
	fields := nginxCombinedLogPattern.FindStringSubmatch(line)
	if fields == nil {
		return RequestLog{}, false
	}
	ip := net.ParseIP(fields[1])
	if ip == nil {
		return RequestLog{}, false
	}
	timestamp, err := time.Parse("02/Jan/2006:15:04:05 -0700", fields[2])
	if err != nil {
		return RequestLog{}, false
	}
	status, err := strconv.Atoi(fields[5])
	if err != nil {
		return RequestLog{}, false
	}
	entry := RequestLog{
		Timestamp: timestamp.Format("2006-01-02 15:04:05 -07:00"),
		IP:        ip.String(),
		Method:    fields[3],
		Path:      fields[4],
		Status:    status,
	}
	if fields[6] != "-" {
		entry.Bytes, err = strconv.ParseInt(fields[6], 10, 64)
		if err != nil || entry.Bytes < 0 {
			return RequestLog{}, false
		}
		entry.HasBytes = true
	}
	return entry, true
}

func matchesRequestLog(entry RequestLog, filters RequestLogFilters) bool {
	if filters.IP != "" && entry.IP != filters.IP {
		return false
	}
	if filters.Method != "" && entry.Method != filters.Method {
		return false
	}
	if filters.Path != "" && !strings.Contains(entry.Path, filters.Path) {
		return false
	}
	switch filters.Status {
	case "":
		return true
	case "errors":
		return entry.Status >= 400
	}
	if len(filters.Status) == 3 && filters.Status[1:] == "xx" {
		return entry.Status/100 == int(filters.Status[0]-'0')
	}
	code, _ := strconv.Atoi(filters.Status)
	return entry.Status == code
}
