package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"path"
	"sync"

	"github.com/gorilla/mux"
)

//go:embed static clients
var assets embed.FS

var indexTmpl = template.Must(
	template.New("index.html").ParseFS(assets, "static/index.html"),
)

type pageData struct {
	Version string
	Host    string
	Clients []clientEntry
}

type clientEntry struct {
	Display string
	Name    string
	URL     string
}

var platforms = []struct {
	GOOS    string
	GOARCH  string
	Display string
}{
	{"linux", "amd64", "Linux / x86_64"},
	{"linux", "arm64", "Linux / ARM64"},
	{"darwin", "amd64", "macOS / Intel"},
	{"darwin", "arm64", "macOS / Apple Silicon"},
	{"windows", "amd64", "Windows / x86_64"},
	{"windows", "arm64", "Windows / ARM64"},
}

func clientFilename(goos, goarch string) string {
	if goos == "windows" {
		return fmt.Sprintf("whim-client-%s-%s.exe", goos, goarch)
	}
	return fmt.Sprintf("whim-client-%s-%s", goos, goarch)
}

var (
	clientsOnce   sync.Once
	cachedClients []clientEntry
)

func availableClients() []clientEntry {
	clientsOnce.Do(func() {
		for _, p := range platforms {
			name := clientFilename(p.GOOS, p.GOARCH)
			f, err := assets.Open("clients/" + name)
			if err == nil {
				f.Close()
				cachedClients = append(cachedClients, clientEntry{
					Display: p.Display,
					Name:    name,
					URL:     "/clients/" + name,
				})
			}
		}
	})
	return cachedClients
}

// RegisterHandlers adds the home page, favicon, and client download routes to r.
func RegisterHandlers(r *mux.Router, version string) {
	r.HandleFunc("/", indexHandler(version)).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/favicon.svg", faviconHandler).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/favicon.svg", http.StatusMovedPermanently)
	}).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/clients/{filename}", clientDownloadHandler).Methods(http.MethodGet, http.MethodHead)
}

func indexHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host == "" {
			host = "localhost:9000"
		}
		data := pageData{
			Version: version,
			Host:    host,
			Clients: availableClients(),
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := indexTmpl.Execute(w, data); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}
}

func faviconHandler(w http.ResponseWriter, r *http.Request) {
	f, err := assets.Open("static/favicon.svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "max-age=86400")
	io.Copy(w, f)
}

func clientDownloadHandler(w http.ResponseWriter, r *http.Request) {
	filename := path.Base(mux.Vars(r)["filename"])
	data, err := assets.ReadFile("clients/" + filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Write(data)
}
