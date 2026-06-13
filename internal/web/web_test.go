package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func newRouter(version string) http.Handler {
	r := mux.NewRouter()
	RegisterHandlers(r, version)
	return r
}

// TestIndexReturns200 checks the home page renders and sets the correct content-type.
func TestIndexReturns200(t *testing.T) {
	ts := httptest.NewServer(newRouter("test-version"))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type: got %q, want text/html", ct)
	}
}

// TestIndexContainsVersion checks the version string is injected into the page.
func TestIndexContainsVersion(t *testing.T) {
	ts := httptest.NewServer(newRouter("v1.2.3"))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "v1.2.3") {
		t.Error("page body does not contain the version string")
	}
}

// TestIndexHostFallback checks that an empty Host header falls back to localhost:9000.
func TestIndexHostFallback(t *testing.T) {
	r := mux.NewRouter()
	RegisterHandlers(r, "dev")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = ""
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "localhost:9000") {
		t.Error("expected fallback host 'localhost:9000' in page body")
	}
	// Plain HTTP request → ws:// and http://
	if !strings.Contains(body, "ws://") {
		t.Error("expected ws:// scheme for plain HTTP request")
	}
}

// TestIndexHTTPSScheme checks that X-Forwarded-Proto: https flips schemes to wss:// and https://.
func TestIndexHTTPSScheme(t *testing.T) {
	r := mux.NewRouter()
	RegisterHandlers(r, "dev")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "wss://") {
		t.Error("expected wss:// scheme when X-Forwarded-Proto: https")
	}
	if !strings.Contains(body, "https://") {
		t.Error("expected https:// scheme when X-Forwarded-Proto: https")
	}
}

// TestIndexHeadAllowed checks HEAD is accepted on /.
func TestIndexHeadAllowed(t *testing.T) {
	ts := httptest.NewServer(newRouter("dev"))
	defer ts.Close()

	resp, err := http.Head(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD /: got %d, want 200", resp.StatusCode)
	}
}

// TestFaviconSVG checks the favicon is served with the right content-type and cache header.
func TestFaviconSVG(t *testing.T) {
	ts := httptest.NewServer(newRouter("dev"))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/favicon.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type: got %q, want image/svg+xml", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "max-age=86400" {
		t.Errorf("Cache-Control: got %q, want max-age=86400", cc)
	}
}

// TestFaviconICORedirects checks /favicon.ico permanently redirects to /favicon.svg.
func TestFaviconICORedirects(t *testing.T) {
	ts := httptest.NewServer(newRouter("dev"))
	defer ts.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse // don't follow redirects
	}}
	resp, err := client.Get(ts.URL + "/favicon.ico")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("status: got %d, want 301", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/favicon.svg" {
		t.Errorf("Location: got %q, want /favicon.svg", loc)
	}
}

// TestClientDownloadNotFound checks that requesting a non-existent binary returns 404.
func TestClientDownloadNotFound(t *testing.T) {
	ts := httptest.NewServer(newRouter("dev"))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/clients/nonexistent-binary")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

// TestClientDownloadPathTraversal checks that path traversal is rejected.
func TestClientDownloadPathTraversal(t *testing.T) {
	ts := httptest.NewServer(newRouter("dev"))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/clients/../static/favicon.svg")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// gorilla/mux cleans the path before routing, so this either 404s or 301s —
	// either way the static file must not be served as a binary download.
	if resp.StatusCode == http.StatusOK &&
		resp.Header.Get("Content-Type") == "application/octet-stream" {
		t.Error("path traversal returned a binary download response")
	}
}

// TestClientFilename checks the filename helper produces the expected names.
func TestClientFilename(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "whim-client-linux-amd64"},
		{"linux", "arm64", "whim-client-linux-arm64"},
		{"darwin", "amd64", "whim-client-darwin-amd64"},
		{"darwin", "arm64", "whim-client-darwin-arm64"},
		{"windows", "amd64", "whim-client-windows-amd64.exe"},
		{"windows", "arm64", "whim-client-windows-arm64.exe"},
	}
	for _, tc := range cases {
		got := clientFilename(tc.goos, tc.goarch)
		if got != tc.want {
			t.Errorf("clientFilename(%q, %q) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

// TestRequestSchemeTLS verifies that r.TLS != nil causes requestScheme to return "https".
func TestRequestSchemeTLS(t *testing.T) {
	ts := httptest.NewTLSServer(newRouter("dev"))
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "wss://") {
		t.Error("expected wss:// scheme for TLS request")
	}
}

// TestClientDownloadSuccess checks that an embedded file in clients/ is served correctly.
func TestClientDownloadSuccess(t *testing.T) {
	ts := httptest.NewServer(newRouter("dev"))
	defer ts.Close()

	// placeholder.txt is always embedded regardless of whether client binaries are built.
	resp, err := http.Get(ts.URL + "/clients/placeholder.txt")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type: got %q, want application/octet-stream", ct)
	}
}
