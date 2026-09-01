// Package updatecheck performs the only automatic WPX project request. It
// reads public GitHub release metadata and never sends panel state, domains,
// accounts, metrics, or a stable installation identifier.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:[-+][0-9A-Za-z.-]+)?$`)

type Result struct {
	Version string
	URL     string
}

type Client struct {
	HTTP *http.Client
}

func (c Client) Check(ctx context.Context, endpoint string) (Result, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "WPX-update-check")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("request GitHub release metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return Result{}, fmt.Errorf("GitHub release API returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
	}
	// GitHub adds fields over time, so strict decoding would make harmless API
	// additions break checks. Decode only the bounded object fields we consume.
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || json.Unmarshal(body, &payload) != nil {
		return Result{}, errors.New("GitHub returned invalid release metadata")
	}
	if payload.Draft || !versionPattern.MatchString(payload.TagName) || !strings.HasPrefix(payload.HTMLURL, "https://github.com/") {
		return Result{}, errors.New("GitHub release metadata failed validation")
	}
	return Result{Version: payload.TagName, URL: payload.HTMLURL}, nil
}

func IsNewer(current, latest string) bool {
	currentParts, currentOK := numericVersion(current)
	latestParts, latestOK := numericVersion(latest)
	if !currentOK || !latestOK {
		return false
	}
	for index := 0; index < 3; index++ {
		if latestParts[index] != currentParts[index] {
			return latestParts[index] > currentParts[index]
		}
	}
	return false
}

func numericVersion(value string) ([3]int, bool) {
	var result [3]int
	match := versionPattern.FindStringSubmatch(value)
	if match == nil {
		return result, false
	}
	for index := 0; index < 3; index++ {
		for _, digit := range match[index+1] {
			result[index] = result[index]*10 + int(digit-'0')
		}
	}
	return result, true
}
