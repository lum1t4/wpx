package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckReadsOnlyValidatedPublicReleaseMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("User-Agent") != "WPX-update-check" {
			t.Errorf("unexpected update request: %s %#v", r.Method, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"v1.4.0","html_url":"https://github.com/lum1t4/wpx/releases/tag/v1.4.0","draft":false,"ignored":"safe"}`))
	}))
	defer server.Close()
	result, err := (Client{HTTP: server.Client()}).Check(context.Background(), server.URL)
	if err != nil || result.Version != "v1.4.0" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestIsNewerUsesSemanticNumericOrder(t *testing.T) {
	for _, test := range []struct {
		current, latest string
		want            bool
	}{
		{"v1.9.0", "v1.10.0", true},
		{"v2.0.0", "v1.99.0", false},
		{"dev", "v1.0.0", false},
		{"v1.2.3", "v1.2.3", false},
	} {
		if got := IsNewer(test.current, test.latest); got != test.want {
			t.Errorf("IsNewer(%q,%q)=%v want %v", test.current, test.latest, got, test.want)
		}
	}
}
