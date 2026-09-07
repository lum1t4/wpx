// Package localize selects a request locale and translates explicitly marked UI copy.
// It never treats arbitrary rendered text as translatable: templates opt static text in
// with a data-i18n attribute, keeping domains, credentials, logs, and user content intact.
package localize

import (
	"bytes"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Locale string

const (
	English    Locale = "en"
	CookieName        = "wpx_locale"
)

type Option struct {
	Tag        Locale
	NativeName string
}

// RequestWriter carries request-local presentation state through the existing
// rendering API without changing global template functions or shared state.
type RequestWriter interface {
	http.ResponseWriter
	Locale() Locale
	CurrentPath() string
}

type requestWriter struct {
	http.ResponseWriter
	locale      Locale
	currentPath string
}

func (w *requestWriter) Locale() Locale              { return w.locale }
func (w *requestWriter) CurrentPath() string         { return w.currentPath }
func (w *requestWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware resolves the locale once per request. The wrapped writer lets the
// renderer retrieve it without a mutable package variable, so concurrent users
// can render different languages safely.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&requestWriter{ResponseWriter: w, locale: Resolve(r), currentPath: currentPath(r)}, r)
	})
}

func Resolve(r *http.Request) Locale {
	if cookie, err := r.Cookie(CookieName); err == nil {
		if locale, ok := canonical(cookie.Value); ok {
			return locale
		}
	}
	best := English
	bestQuality := -1.0
	for _, languageRange := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		parts := strings.Split(languageRange, ";")
		tag := strings.TrimSpace(parts[0])
		quality := 1.0
		for _, parameter := range parts[1:] {
			parameter = strings.TrimSpace(parameter)
			if strings.HasPrefix(parameter, "q=") {
				parsed, err := strconv.ParseFloat(strings.TrimPrefix(parameter, "q="), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if quality <= bestQuality || quality == 0 {
			continue
		}
		if locale, ok := canonical(tag); ok {
			best, bestQuality = locale, quality
			continue
		}
		if cut := strings.IndexByte(tag, '-'); cut > 0 {
			if locale, ok := canonical(tag[:cut]); ok {
				best, bestQuality = locale, quality
			}
		}
	}
	return best
}

func SwitchHandler(w http.ResponseWriter, r *http.Request) {
	locale, ok := canonical(r.URL.Query().Get("lang"))
	if !ok {
		http.Error(w, "unsupported language", http.StatusBadRequest)
		return
	}
	next := safeNext(r.URL.Query().Get("next"))
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: string(locale), Path: "/", MaxAge: int((365 * 24 * time.Hour).Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func currentPath(r *http.Request) string {
	if r.URL == nil {
		return "/"
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	return path
}

func safeNext(raw string) string {
	if raw == "" || strings.Contains(raw, "\\") {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return u.RequestURI()
}

func canonical(raw string) (Locale, bool) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "_", "-")
	for _, option := range options {
		if strings.EqualFold(string(option.Tag), raw) {
			return option.Tag, true
		}
	}
	return "", false
}

func Supported() []Option {
	result := make([]Option, len(options))
	copy(result, options)
	return result
}

// T returns English explicitly when a locale or key is incomplete. This makes
// partial catalog updates visible and deterministic instead of returning a key.
func T(locale Locale, key string) string {
	english, ok := catalog[English][key]
	if !ok {
		return key
	}
	if translated := catalog[locale][key]; translated != "" {
		return translated
	}
	return english
}

// TranslateHTML replaces the plain-text contents of elements explicitly marked
// data-i18n="key". Marked elements containing child markup are left untouched;
// templates should put the marker on a leaf span. This intentionally narrow
// parser keeps all unmarked rendered values byte-for-byte unchanged.
func TranslateHTML(input []byte, locale Locale) []byte {
	if locale == English || len(input) == 0 {
		return input
	}
	var output bytes.Buffer
	protected := ""
	for at := 0; at < len(input); {
		startRel := bytes.IndexByte(input[at:], '<')
		if startRel < 0 {
			output.Write(input[at:])
			break
		}
		start := at + startRel
		output.Write(input[at:start])
		end := tagEnd(input, start)
		if end < 0 {
			output.Write(input[start:])
			break
		}
		name, closing := tagName(input[start : end+1])
		if protected != "" {
			output.Write(input[start : end+1])
			if closing && name == protected {
				protected = ""
			}
			at = end + 1
			continue
		}
		if !closing && isProtected(name) {
			protected = name
			output.Write(input[start : end+1])
			at = end + 1
			continue
		}
		key, marked := attributeValue(input[start:end+1], "data-i18n")
		if !closing && marked && name != "" {
			if _, known := englishCatalog[key]; !known {
				output.Write(input[start : end+1])
				at = end + 1
				continue
			}
			contentEndRel := bytes.IndexByte(input[end+1:], '<')
			if contentEndRel >= 0 {
				contentEnd := end + 1 + contentEndRel
				closeEnd := tagEnd(input, contentEnd)
				if closeEnd >= 0 {
					closeName, isClosing := tagName(input[contentEnd : closeEnd+1])
					if isClosing && closeName == name {
						output.Write(input[start : end+1])
						output.WriteString(html.EscapeString(T(locale, key)))
						output.Write(input[contentEnd : closeEnd+1])
						at = closeEnd + 1
						continue
					}
				}
			}
		}
		output.Write(input[start : end+1])
		at = end + 1
	}
	return output.Bytes()
}

func tagEnd(input []byte, start int) int {
	var quote byte
	for index := start + 1; index < len(input); index++ {
		if quote != 0 {
			if input[index] == quote {
				quote = 0
			}
			continue
		}
		if input[index] == '\'' || input[index] == '"' {
			quote = input[index]
		} else if input[index] == '>' {
			return index
		}
	}
	return -1
}

func tagName(tag []byte) (string, bool) {
	if len(tag) < 3 || tag[0] != '<' || tag[1] == '!' || tag[1] == '?' {
		return "", false
	}
	index := 1
	closing := false
	if tag[index] == '/' {
		closing = true
		index++
	}
	start := index
	for index < len(tag) && ((tag[index] >= 'a' && tag[index] <= 'z') || (tag[index] >= 'A' && tag[index] <= 'Z') || (index > start && tag[index] >= '0' && tag[index] <= '9')) {
		index++
	}
	return strings.ToLower(string(tag[start:index])), closing
}

func attributeValue(tag []byte, wanted string) (string, bool) {
	index := 1
	if index < len(tag) && tag[index] == '/' {
		index++
	}
	for index < len(tag) && ((tag[index] >= 'a' && tag[index] <= 'z') || (tag[index] >= 'A' && tag[index] <= 'Z') || (tag[index] >= '0' && tag[index] <= '9')) {
		index++
	}
	for index < len(tag) {
		for index < len(tag) && (tag[index] == ' ' || tag[index] == '\t' || tag[index] == '\r' || tag[index] == '\n') {
			index++
		}
		start := index
		for index < len(tag) && ((tag[index] >= 'a' && tag[index] <= 'z') || (tag[index] >= 'A' && tag[index] <= 'Z') || tag[index] == '-' || tag[index] == '_' || (tag[index] >= '0' && tag[index] <= '9')) {
			index++
		}
		name := strings.ToLower(string(tag[start:index]))
		for index < len(tag) && (tag[index] == ' ' || tag[index] == '\t' || tag[index] == '\r' || tag[index] == '\n') {
			index++
		}
		if index >= len(tag) || tag[index] != '=' {
			index++
			continue
		}
		index++
		for index < len(tag) && (tag[index] == ' ' || tag[index] == '\t') {
			index++
		}
		if index >= len(tag) || (tag[index] != '\'' && tag[index] != '"') {
			continue
		}
		quote := tag[index]
		index++
		valueStart := index
		for index < len(tag) && tag[index] != quote {
			index++
		}
		if name == wanted && index < len(tag) {
			return string(tag[valueStart:index]), true
		}
		index++
	}
	return "", false
}

func isProtected(name string) bool {
	switch name {
	case "script", "style", "textarea", "pre", "code":
		return true
	default:
		return false
	}
}
