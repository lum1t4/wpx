package localize

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogHasThirtyThreeUsefulLocales(t *testing.T) {
	if got := len(Supported()); got != 33 {
		t.Fatalf("supported locales = %d, want 33", got)
	}
	for _, option := range Supported() {
		translations := catalog[option.Tag]
		if len(translations) != len(englishCatalog) {
			t.Errorf("locale %q has %d translations, want %d", option.Tag, len(translations), len(englishCatalog))
		}
		if option.Tag != English && translations["overview"] == englishCatalog["overview"] {
			t.Errorf("locale %q aliases English overview", option.Tag)
		}
	}
}

func TestFileManagerVocabularyIsLocalized(t *testing.T) {
	tests := []struct {
		locale Locale
		key    string
		want   string
	}{
		{locale: "it", key: "new_folder", want: "Nuova cartella"},
		{locale: "de", key: "clear_selection", want: "Auswahl aufheben"},
		{locale: "ar", key: "more_actions", want: "إجراءات إضافية"},
		{locale: "ja", key: "archive", want: "アーカイブ"},
		{locale: "pt-BR", key: "extract", want: "Extrair"},
	}
	for _, test := range tests {
		if got := T(test.locale, test.key); got != test.want {
			t.Errorf("T(%q, %q) = %q, want %q", test.locale, test.key, got, test.want)
		}
	}

	input := []byte(`<span data-i18n="selected">Selected</span> <span id="files-selection-count">3</span>`)
	want := `<span data-i18n="selected">Selezionati</span> <span id="files-selection-count">3</span>`
	if got := string(TranslateHTML(input, "it")); got != want {
		t.Fatalf("translated selection state = %q, want %q", got, want)
	}
}

func TestAppearanceAndLogVocabularyIsLocalized(t *testing.T) {
	for _, option := range Supported() {
		for _, key := range commonKeys {
			if got := catalog[option.Tag][key]; strings.TrimSpace(got) == "" {
				t.Errorf("locale %q has no %q translation", option.Tag, key)
			}
		}
	}

	tests := []struct {
		locale Locale
		key    string
		want   string
	}{
		{locale: "it", key: "appearance", want: "Aspetto"},
		{locale: "de", key: "system", want: "System"},
		{locale: "ar", key: "dark", want: "داكن"},
		{locale: "ja", key: "all_requests", want: "すべてのリクエスト"},
		{locale: "pt-BR", key: "apply_filters", want: "Aplicar filtros"},
		{locale: "zh-TW", key: "schedule", want: "排程"},
		{locale: "it", key: "account_settings", want: "Impostazioni account"},
		{locale: "ar", key: "account_settings", want: "إعدادات الحساب"},
		{locale: "ja", key: "account_settings", want: "アカウント設定"},
		{locale: "fr", key: "account", want: "Compte"},
		{locale: "es", key: "settings", want: "Ajustes"},
		{locale: "it", key: "slack", want: "Slack"},
		{locale: "zh-CN", key: "telegram", want: "Telegram"},
	}
	for _, test := range tests {
		if got := T(test.locale, test.key); got != test.want {
			t.Errorf("T(%q, %q) = %q, want %q", test.locale, test.key, got, test.want)
		}
	}
}

func TestResolveCookieThenLanguageHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Language", "fr-CA;q=0.9,en;q=0.8")
	if got := Resolve(r); got != "fr" {
		t.Fatalf("header locale = %q", got)
	}
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "pt_br"})
	if got := Resolve(r); got != "pt-BR" {
		t.Fatalf("cookie locale = %q", got)
	}
}

func TestResolveHonorsLanguageQuality(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Language", "fr;q=0, de;q=0.5, it;q=0.9")
	if got := Resolve(r); got != "it" {
		t.Fatalf("weighted locale = %q", got)
	}
}

func TestTranslateHTMLOnlyMarkedLeafText(t *testing.T) {
	input := []byte(`<p data-i18n="overview">Overview</p><p>Overview</p><p data-i18n="unknown">Future English</p><p data-i18n="sites"><strong>Sites</strong></p><code data-i18n="sites">secret</code>`)
	got := string(TranslateHTML(input, "it"))
	want := `<p data-i18n="overview">Panoramica</p><p>Overview</p><p data-i18n="unknown">Future English</p><p data-i18n="sites"><strong>Sites</strong></p><code data-i18n="sites">secret</code>`
	if got != want {
		t.Fatalf("translated HTML:\n%s\nwant:\n%s", got, want)
	}
}

func TestMissingKeyFallsBackExplicitly(t *testing.T) {
	if got := T("it", "future_label"); got != "future_label" {
		t.Fatalf("unknown key = %q", got)
	}
	delete(catalog["it"], "overview")
	t.Cleanup(func() { catalog["it"]["overview"] = "Panoramica" })
	if got := T("it", "overview"); got != "Overview" {
		t.Fatalf("missing translation = %q", got)
	}
}

func TestSwitchHandlerSetsSecureCookieAndRejectsExternalRedirect(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/language?lang=de&next=https://attacker.example", nil)
	w := httptest.NewRecorder()
	SwitchHandler(w, r)
	response := w.Result()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/" {
		t.Fatalf("status/location = %d %q", response.StatusCode, response.Header.Get("Location"))
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie is not securely scoped: %#v", cookies)
	}

	r = httptest.NewRequest(http.MethodGet, "/language?lang=xx", nil)
	w = httptest.NewRecorder()
	SwitchHandler(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unsupported") {
		t.Fatalf("unsupported response = %d %q", w.Code, w.Body.String())
	}
}

func TestMiddlewareCarriesIndependentRequestState(t *testing.T) {
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, ok := w.(RequestWriter)
		if !ok {
			t.Fatal("writer lacks request localization state")
		}
		_, _ = w.Write([]byte(string(state.Locale()) + " " + state.CurrentPath()))
	}))
	r := httptest.NewRequest(http.MethodGet, "/sites?q=one", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "ja"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if got := w.Body.String(); got != "ja /sites?q=one" {
		t.Fatalf("request state = %q", got)
	}
}
