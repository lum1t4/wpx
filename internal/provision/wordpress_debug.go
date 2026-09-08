package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

type pinnedPHPFileLinter interface {
	LintPHPFile(context.Context, Identity, string, *os.File) error
}

const (
	wpDebugStart = "/* WPX WORDPRESS DEBUG START */"
	wpDebugEnd   = "/* WPX WORDPRESS DEBUG END */"
)

var (
	wpDebugBlock    = regexp.MustCompile(`(?s)/\* WPX WORDPRESS DEBUG START \*/\r?\n.*?\r?\n/\* WPX WORDPRESS DEBUG END \*/\r?\n(?:\r?\n)?`)
	wpDebugNearby   = regexp.MustCompile(`(?s)^(?i:define).{0,256}['\"](?:WP_DEBUG|WP_DEBUG_LOG|WP_DEBUG_DISPLAY)['\"]`)
	wpDebugSimple   = regexp.MustCompile(`^(?i:define)\s*\(\s*(?:'(WP_DEBUG|WP_DEBUG_LOG|WP_DEBUG_DISPLAY)'|"(WP_DEBUG|WP_DEBUG_LOG|WP_DEBUG_DISPLAY)")\s*,\s*(true|false|'(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*")\s*\)\s*;\s*((?://|#).*)?$`)
	phpHeredocStart = regexp.MustCompile(`<<<[ \t]*(?:'([A-Za-z_][A-Za-z0-9_]*)'|"([A-Za-z_][A-Za-z0-9_]*)"|([A-Za-z_][A-Za-z0-9_]*))[ \t]*;?[ \t]*$`)
)

func wordpressDebugBlock(logPath string, enabled bool) string {
	debug, log := "false", "false"
	if enabled {
		debug, log = "true", "'"+strings.ReplaceAll(logPath, "'", "\\'")+"'"
	}
	return wpDebugStart + "\n" +
		"define('WP_DEBUG', " + debug + ");\n" +
		"define('WP_DEBUG_LOG', " + log + ");\n" +
		"define('WP_DEBUG_DISPLAY', false);\n" +
		wpDebugEnd + "\n"
}

func updateWordPressDebug(content []byte, enabled bool, logPath string) ([]byte, bool, error) {
	startCount, endCount := bytes.Count(content, []byte(wpDebugStart)), bytes.Count(content, []byte(wpDebugEnd))
	owned := wpDebugBlock.Find(content)
	if startCount != endCount || startCount > 1 || (startCount == 1 && owned == nil) {
		return nil, false, errors.New("wp-config.php contains a partial or duplicate WPX debug block")
	}
	withoutOwned := wpDebugBlock.ReplaceAll(content, nil)
	if owned != nil {
		validEnabled := bytes.Equal(bytes.TrimSpace(owned), bytes.TrimSpace([]byte(wordpressDebugBlock(logPath, true))))
		validDisabled := bytes.Equal(bytes.TrimSpace(owned), bytes.TrimSpace([]byte(wordpressDebugBlock(logPath, false))))
		if !validEnabled && !validDisabled {
			return nil, false, errors.New("wp-config.php contains a modified WPX debug block")
		}
	}
	cleaned, values, err := adoptSimpleWordPressDebugDefines(withoutOwned)
	if err != nil {
		return nil, false, err
	}
	if owned != nil && len(values) != 0 {
		return nil, false, errors.New("wp-config.php contains debug definitions outside the WPX block")
	}
	block := []byte(wordpressDebugBlock(logPath, enabled))
	anchor := []byte("/* That's all, stop editing!")
	index := bytes.Index(cleaned, anchor)
	if index < 0 {
		return nil, false, errors.New("wp-config.php does not contain the WordPress configuration boundary")
	}
	updated := append([]byte{}, cleaned[:index]...)
	updated = append(updated, block...)
	updated = append(updated, '\n')
	updated = append(updated, cleaned[index:]...)
	return updated, !bytes.Equal(updated, content), nil
}

func inspectWordPressDebug(content []byte, logPath string) (bool, bool, bool) {
	startCount, endCount := bytes.Count(content, []byte(wpDebugStart)), bytes.Count(content, []byte(wpDebugEnd))
	owned := wpDebugBlock.Find(content)
	if startCount != endCount || startCount > 1 || (startCount == 1 && owned == nil) {
		return false, false, false
	}
	withoutOwned := wpDebugBlock.ReplaceAll(content, nil)
	_, values, err := adoptSimpleWordPressDebugDefines(withoutOwned)
	if err != nil {
		return false, false, false
	}
	if owned != nil && len(values) != 0 {
		return false, false, false
	}
	if owned == nil {
		debugEnabled := values["WP_DEBUG"] == "true"
		return debugEnabled, true, false
	}
	if bytes.Equal(bytes.TrimSpace(owned), bytes.TrimSpace([]byte(wordpressDebugBlock(logPath, true)))) {
		return true, true, true
	}
	if bytes.Equal(bytes.TrimSpace(owned), bytes.TrimSpace([]byte(wordpressDebugBlock(logPath, false)))) {
		return false, true, true
	}
	return false, false, false
}

// adoptSimpleWordPressDebugDefines recognizes define calls with literal values
// without executing wp-config.php. It removes one simple definition per debug
// constant so a standard WordPress config can be adopted. Any duplicate,
// expression, conditional call, or malformed call fails closed. PHP comments
// and strings are skipped by scanPHPDefineTokens.
func adoptSimpleWordPressDebugDefines(content []byte) ([]byte, map[string]string, error) {
	type replacement struct {
		start, end int
		comment    string
	}
	var replacements []replacement
	seen := map[string]bool{}
	values := map[string]string{}
	heredocs, err := scanPHPHeredocs(content)
	if err != nil {
		return nil, nil, err
	}
	for _, offset := range scanPHPDefineTokens(content, heredocs) {
		lineStart := bytes.LastIndexByte(content[:offset], '\n') + 1
		lineEnd := len(content)
		if newline := bytes.IndexByte(content[offset:], '\n'); newline >= 0 {
			lineEnd = offset + newline + 1
		}
		prefix := content[lineStart:offset]
		probeEnd := offset + 1024
		if probeEnd > len(content) {
			probeEnd = len(content)
		}
		if !wpDebugNearby.Match(content[offset:probeEnd]) {
			continue
		}
		if string(content[offset:offset+6]) != "define" {
			return nil, nil, errors.New("wp-config.php contains an unsupported debug definition")
		}
		candidate := strings.TrimSpace(string(content[offset:lineEnd]))
		if len(bytes.TrimSpace(prefix)) != 0 {
			return nil, nil, errors.New("wp-config.php contains a conditional or inline debug definition")
		}
		match := wpDebugSimple.FindStringSubmatch(candidate)
		if match == nil {
			return nil, nil, errors.New("wp-config.php contains a non-literal debug definition")
		}
		name := match[1]
		if name == "" {
			name = match[2]
		}
		value := match[3]
		if (name == "WP_DEBUG" || name == "WP_DEBUG_DISPLAY") && value != "true" && value != "false" {
			return nil, nil, errors.New("wp-config.php contains a non-boolean debug definition")
		}
		if seen[name] {
			return nil, nil, errors.New("wp-config.php contains duplicate debug definitions")
		}
		seen[name] = true
		values[name] = value
		replacements = append(replacements, replacement{lineStart, lineEnd, match[4]})
	}
	if len(replacements) == 0 {
		return content, values, nil
	}
	var cleaned bytes.Buffer
	position := 0
	for _, item := range replacements {
		cleaned.Write(content[position:item.start])
		if item.comment != "" {
			cleaned.WriteString(item.comment)
			cleaned.WriteByte('\n')
		}
		position = item.end
	}
	cleaned.Write(content[position:])
	return cleaned.Bytes(), values, nil
}

type phpSourceRange struct{ start, end int }

func scanPHPHeredocs(content []byte) ([]phpSourceRange, error) {
	var ranges []phpSourceRange
	for position := 0; position < len(content); {
		width := bytes.IndexByte(content[position:], '\n')
		if width < 0 {
			width = len(content) - position
		} else {
			width++
		}
		lineEnd := position + width
		match := phpHeredocStart.FindStringSubmatch(strings.TrimSpace(string(content[position:lineEnd])))
		if match == nil {
			position = lineEnd
			continue
		}
		label := match[1]
		if label == "" {
			label = match[2]
		}
		if label == "" {
			label = match[3]
		}
		bodyStart, cursor, found := lineEnd, lineEnd, false
		for cursor < len(content) {
			nextWidth := bytes.IndexByte(content[cursor:], '\n')
			if nextWidth < 0 {
				nextWidth = len(content) - cursor
			} else {
				nextWidth++
			}
			terminatorEnd := cursor + nextWidth
			trimmed := strings.TrimSpace(string(content[cursor:terminatorEnd]))
			if trimmed == label || trimmed == label+";" {
				if bytes.Contains(content[bodyStart:cursor], []byte("WP_DEBUG")) {
					return nil, errors.New("wp-config.php contains a debug constant name inside heredoc or nowdoc syntax")
				}
				ranges = append(ranges, phpSourceRange{position, terminatorEnd})
				position, found = terminatorEnd, true
				break
			}
			cursor = terminatorEnd
		}
		if !found {
			return nil, errors.New("wp-config.php contains unterminated heredoc or nowdoc syntax")
		}
	}
	return ranges, nil
}

func scanPHPDefineTokens(content []byte, heredocs []phpSourceRange) []int {
	const (
		normal = iota
		singleQuoted
		doubleQuoted
		backtickQuoted
		lineComment
		blockComment
	)
	state := normal
	var offsets []int
	for i := 0; i < len(content); i++ {
		inHeredoc := false
		for _, heredoc := range heredocs {
			if i >= heredoc.start && i < heredoc.end {
				i, inHeredoc = heredoc.end-1, true
				break
			}
		}
		if inHeredoc {
			continue
		}
		current := content[i]
		switch state {
		case singleQuoted, doubleQuoted, backtickQuoted:
			quote := byte('\'')
			if state == doubleQuoted {
				quote = '"'
			} else if state == backtickQuoted {
				quote = '`'
			}
			if current == '\\' {
				i++
				continue
			}
			if current == quote {
				state = normal
			}
		case lineComment:
			if current == '\n' {
				state = normal
			}
		case blockComment:
			if current == '*' && i+1 < len(content) && content[i+1] == '/' {
				state = normal
				i++
			}
		default:
			if current == '\'' {
				state = singleQuoted
				continue
			}
			if current == '"' {
				state = doubleQuoted
				continue
			}
			if current == '`' {
				state = backtickQuoted
				continue
			}
			if current == '#' {
				state = lineComment
				continue
			}
			if current == '/' && i+1 < len(content) && content[i+1] == '/' {
				state = lineComment
				i++
				continue
			}
			if current == '/' && i+1 < len(content) && content[i+1] == '*' {
				state = blockComment
				i++
				continue
			}
			if i+6 <= len(content) && strings.EqualFold(string(content[i:i+6]), "define") && (i == 0 || !isPHPIdentifierByte(content[i-1])) && (i+6 == len(content) || !isPHPIdentifierByte(content[i+6])) {
				offsets = append(offsets, i)
			}
		}
	}
	return offsets
}

func isPHPIdentifierByte(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func (h *Host) wordpressDebugLogPath(site model.Site) string {
	return filepath.Join(h.SiteRoot, site.ID, "tmp", "wordpress-debug.log")
}

func (h *Host) validateWordPressDebug(site model.Site) error {
	if err := model.ValidateWordPressDebugSite(site); err != nil {
		return err
	}
	if err := h.validate(); err != nil {
		return err
	}
	return ensureContained(h.SiteRoot, h.wordpressDebugLogPath(site))
}

func (h *Host) WordPressDebugStatus(ctx context.Context, site model.Site) (model.WordPressDebugStatus, error) {
	if err := h.validateWordPressDebug(site); err != nil {
		return model.WordPressDebugStatus{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	content, err := secureReadWordPressConfig(h, site)
	if err != nil {
		return model.WordPressDebugStatus{}, err
	}
	enabled, known, managed := inspectWordPressDebug(content, h.wordpressDebugLogPath(site))
	status, err := secureWordPressDebugLogStatus(h, site)
	if err != nil {
		return model.WordPressDebugStatus{}, err
	}
	status.Enabled, status.Known, status.Managed = enabled, known, managed
	return status, nil
}

func (h *Host) SetWordPressDebug(ctx context.Context, site model.Site, enabled bool) error {
	if err := h.validateWordPressDebug(site); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	identity, err := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err != nil {
		return err
	}
	created := false
	if enabled {
		created, err = securePrepareWordPressDebugLog(h, site, identity)
		if err != nil {
			return fmt.Errorf("prepare private WordPress debug log: %w", err)
		}
	}
	if err := secureSetWordPressDebug(ctx, h, site, identity, enabled, h.wordpressDebugLogPath(site)); err != nil {
		if created {
			_ = secureRemoveWordPressDebugLog(h, site)
		}
		return err
	}
	return nil
}

func (h *Host) ReadWordPressDebugLog(ctx context.Context, site model.Site) (model.WordPressDebugLog, error) {
	if err := h.validateWordPressDebug(site); err != nil {
		return model.WordPressDebugLog{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return secureReadWordPressDebugLog(h, site, model.WordPressDebugLogLimit)
}

func (h *Host) ClearWordPressDebugLog(ctx context.Context, site model.Site) error {
	if err := h.validateWordPressDebug(site); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return secureClearWordPressDebugLog(h, site)
}
