package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
)

var (
	staticAssetVersionsOnce sync.Once
	staticAssetVersions     map[string]string
)

func loadStaticAssetVersions() map[string]string {
	staticAssetVersionsOnce.Do(func() {
		staticAssetVersions = make(map[string]string)
		entries, err := fs.ReadDir(templateFiles, "static")
		if err != nil {
			panic("read embedded static assets: " + err.Error())
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			contents, err := fs.ReadFile(templateFiles, "static/"+entry.Name())
			if err != nil {
				panic("read embedded static asset: " + err.Error())
			}
			digest := sha256.Sum256(contents)
			staticAssetVersions[entry.Name()] = hex.EncodeToString(digest[:8])
		}
	})
	return staticAssetVersions
}

// assetURL gives templates a deployment-specific URL. A new executable changes
// the URL whenever an embedded asset changes, so browsers can safely retain the
// matching stylesheet and scripts across full-page navigation.
func assetURL(name string) string {
	name = path.Base(name)
	version, ok := loadStaticAssetVersions()[name]
	if !ok {
		return "/assets/" + name
	}
	return "/assets/" + name + "?v=" + version
}

func staticAssetHandler(files fs.FS) http.Handler {
	serve := http.FileServer(http.FS(files))
	versions := loadStaticAssetVersions()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		version, known := versions[name]
		known = known && strings.TrimPrefix(r.URL.Path, "/") == name
		if known {
			etag := `"sha256-` + version + `"`
			w.Header().Set("ETag", etag)
			if r.URL.Query().Get("v") == version {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		serve.ServeHTTP(w, r)
	})
}
