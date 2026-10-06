package api_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evanw/esbuild/pkg/api"
)

const catalogHTTPTimeout = 5 * time.Second

type catalogServer struct {
	Context  api.BuildContext
	Client   *http.Client
	URL      string
	Requests chan api.ServeOnRequestArgs
}

type catalogResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func catalogStartServer(t *testing.T, build api.BuildOptions, serve api.ServeOptions) catalogServer {
	t.Helper()
	build.LogLevel = api.LogLevelSilent
	ctx, err := api.Context(build)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctx.Dispose)
	requests := make(chan api.ServeOnRequestArgs, 64)
	serve.Host = "127.0.0.1"
	serve.Port = -1 // The native API maps a negative port to an ephemeral port.
	serve.OnRequest = func(args api.ServeOnRequestArgs) { requests <- args }
	result, serveErr := ctx.Serve(serve)
	if serveErr != nil {
		t.Fatal(serveErr)
	}
	transport := &http.Transport{DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: catalogHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	protocol := "http://"
	if serve.Keyfile != "" && serve.Certfile != "" {
		protocol = "https://"
	}
	return catalogServer{Context: ctx, Client: client, URL: protocol + net.JoinHostPort("127.0.0.1", fmt.Sprint(result.Port)), Requests: requests}
}

func catalogRequest(t *testing.T, server catalogServer, request *http.Request) catalogResponse {
	t.Helper()
	response, err := server.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	const responseLimit = 1 << 20
	body, readErr := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > responseLimit {
		t.Fatalf("read bounded native response: read=%v close=%v bytes=%d", readErr, closeErr, len(body))
	}
	select {
	case event := <-server.Requests:
		if event.Method != request.Method || event.Path != request.URL.Path || event.Status != response.StatusCode || event.RemoteAddress == "" || event.TimeInMS < 0 {
			t.Fatalf("native request callback lost response identity: %+v", event)
		}
	case <-time.After(catalogHTTPTimeout):
		t.Fatal("native request callback did not complete")
	}
	return catalogResponse{Status: response.StatusCode, Header: response.Header, Body: body}
}

func catalogHTTPRequest(t *testing.T, server catalogServer, method, path string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func catalogRunNativeChild(t *testing.T, selector, childFlag string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childContext, cancel := context.WithTimeout(t.Context(), 2*catalogHTTPTimeout)
	defer cancel()
	command := exec.CommandContext(childContext, executable, "-test.run="+selector, "-test.timeout=10s")
	command.Env = append(os.Environ(), childFlag+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("joined native lifecycle process: %v\n%s", err, output)
	}
}

func TestCatalogAPIContextHTTP(t *testing.T) {
	dir := t.TempDir()
	entry := catalogWriteFile(t, dir, "entry.ts", `export const result: string = "native-served";`)
	static := "native-static-bytes"
	catalogWriteFile(t, dir, "public/static.bin", static)
	catalogWriteFile(t, dir, "public/indexed/index.html", "<h1>native index</h1>")
	catalogWriteFile(t, dir, "public/list/quoted&'.txt", "escaped filename")
	catalogWriteFile(t, dir, "public/list/child/one.txt", "child file")
	server := catalogStartServer(t,
		api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outdir: "public/build/nested", Format: api.FormatESModule},
		api.ServeOptions{Servedir: "public", CORS: api.CORSOptions{Origin: []string{"https://allowed.test", "https://*.allowed.test"}}})
	tests := []struct {
		name       string
		method     string
		path       string
		host       string
		rangeValue string
		origin     string
		status     int
		body       string
	}{
		{"generated", "GET", "/build/nested/entry.js", "", "", "", http.StatusOK, "native-served"},
		{"head", "HEAD", "/static.bin", "", "", "", http.StatusOK, ""},
		{"static", "GET", "/static.bin", "", "", "", http.StatusOK, static},
		{"range", "GET", "/static.bin", "", "bytes=0-5", "", http.StatusPartialContent, static[:6]},
		{"single-byte", "GET", "/static.bin", "", "bytes=2-2", "", http.StatusPartialContent, static[2:3]},
		{"bad-range-prefix", "GET", "/static.bin", "", "items=0-5", "", http.StatusOK, static},
		{"bad-range-start", "GET", "/static.bin", "", "bytes=-5", "", http.StatusOK, static},
		{"bad-range-end", "GET", "/static.bin", "", "bytes=0-", "", http.StatusOK, static},
		{"bad-range-integer", "GET", "/static.bin", "", "bytes=a-5", "", http.StatusOK, static},
		{"bad-range-reversed", "GET", "/static.bin", "", "bytes=5-2", "", http.StatusOK, static},
		{"bad-range-bound", "GET", "/static.bin", "", "bytes=0-99999", "", http.StatusOK, static},
		{"directory-redirect", "GET", "/indexed", "", "", "", http.StatusFound, ""},
		{"index", "GET", "/indexed/", "", "", "", http.StatusOK, "native index"},
		{"listing", "GET", "/list/", "", "", "", http.StatusOK, "quoted&amp;&apos;.txt"},
		{"virtual-directory", "GET", "/build/nested/", "", "", "", http.StatusOK, "entry.js"},
		{"root-listing", "GET", "/", "", "", "", http.StatusOK, "build/"},
		{"localhost", "GET", "/static.bin", "localhost", "", "", http.StatusOK, static},
		{"host-denied", "GET", "/static.bin", "outside.test", "", "", http.StatusForbidden, "host"},
		{"backslash-denied", "GET", "/bad%5Cpath", "", "", "", http.StatusBadRequest, "Bad Request"},
		{"not-found", "GET", "/missing", "", "", "", http.StatusNotFound, "Not Found"},
		{"method", "POST", "/static.bin", "", "", "", http.StatusNotFound, "Not Found"},
		{"exact-origin", "GET", "/static.bin", "", "", "https://allowed.test", http.StatusOK, static},
		{"wildcard-origin", "GET", "/static.bin", "", "", "https://child.allowed.test", http.StatusOK, static},
		{"denied-origin", "GET", "/static.bin", "", "", "https://outside.test", http.StatusOK, static},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := catalogHTTPRequest(t, server, tt.method, tt.path)
			if tt.host != "" {
				request.Host = tt.host
			}
			request.Header.Set("Range", tt.rangeValue)
			request.Header.Set("Origin", tt.origin)
			response := catalogRequest(t, server, request)
			if response.Status != tt.status {
				t.Fatalf("native HTTP status=%d expected=%d body=%q", response.Status, tt.status, response.Body)
			}
			if tt.body == "" {
				if len(response.Body) != 0 {
					t.Fatalf("body must be empty: %q", response.Body)
				}
			} else {
				catalogRequireText(t, string(response.Body), tt.body)
			}
			if tt.status == http.StatusPartialContent && response.Header.Get("Content-Range") == "" {
				t.Fatal("native partial response lost byte range metadata")
			}
			if tt.name == "directory-redirect" && response.Header.Get("Location") != "/indexed/" {
				t.Fatalf("native directory redirect lost its target: %+v", response.Header)
			}
			if tt.name == "exact-origin" || tt.name == "wildcard-origin" {
				if response.Header.Get("Access-Control-Allow-Origin") != tt.origin {
					t.Fatal("allowed origin not forwarded")
				}
			} else if tt.name == "denied-origin" && response.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("denied origin was authorized")
			}
		})
	}
	request := catalogHTTPRequest(t, server, "GET", "/favicon.ico")
	request.Header.Set("Accept-Encoding", "br, gzip;q=1")
	favicon := catalogRequest(t, server, request)
	if favicon.Status != http.StatusOK || favicon.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("native compressed favicon missing")
	}
	reader, err := gzip.NewReader(bytes.NewReader(favicon.Body))
	if err != nil {
		t.Fatal(err)
	}
	icon, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(icon) < 4 || !bytes.Equal(icon[:4], []byte{0, 0, 1, 0}) {
		t.Fatalf("invalid native icon: read=%v close=%v", readErr, closeErr)
	}
	if _, err := server.Context.Serve(api.ServeOptions{Host: "127.0.0.1", Port: -1}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("duplicate serve accepted: %v", err)
	}
	server.Context.Dispose()
	if _, err := server.Context.Serve(api.ServeOptions{}); err == nil || !strings.Contains(err.Error(), "disposed") {
		t.Fatalf("disposed context accepted serve: %v", err)
	}
	if response, err := server.Client.Get(server.URL + "/static.bin"); err == nil {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		t.Fatal("disposed native server still accepted connections")
	}
	if _, err := os.Stat(filepath.Join(dir, "public/build")); !os.IsNotExist(err) {
		t.Fatalf("in-memory served output written to disk: %v", err)
	}
}

func TestCatalogAPIContextServeOptions(t *testing.T) {
	tests := []struct {
		name  string
		build api.BuildOptions
		serve api.ServeOptions
		want  string
	}{
		{"key-without-cert", api.BuildOptions{}, api.ServeOptions{Keyfile: "key.pem"}, "both key and certificate"},
		{"cert-without-key", api.BuildOptions{}, api.ServeOptions{Certfile: "cert.pem"}, "both key and certificate"},
		{"multiple-origin-stars", api.BuildOptions{}, api.ServeOptions{CORS: api.CORSOptions{Origin: []string{"https://*.*.test"}}}, "Invalid origin"},
		{"output-outside-servedir", api.BuildOptions{EntryPoints: []string{"entry.ts"}, Outdir: "out"}, api.ServeOptions{Servedir: "public"}, "must be contained"},
		{"entry-without-output", api.BuildOptions{EntryPoints: []string{"entry.ts"}}, api.ServeOptions{}, "without an output path"},
		{"tls-source-missing", api.BuildOptions{}, api.ServeOptions{Keyfile: "absent-key.pem", Certfile: "absent-cert.pem"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const tlsFailureChild = "CATALOG_API_TLS_FAILURE_CHILD"
			if tt.name == "tls-source-missing" && os.Getenv(tlsFailureChild) != "1" {
				// Failed native TLS startup has no context-owned listener cleanup.
				// A joined child process confines the retained upstream lifetime.
				catalogRunNativeChild(t, "^TestCatalogAPIContextServeOptions$/^tls-source-missing$", tlsFailureChild)
				return
			}
			tt.build.AbsWorkingDir = t.TempDir()
			tt.build.LogLevel = api.LogLevelSilent
			ctx, err := api.Context(tt.build)
			if err != nil {
				t.Fatal(err)
			}
			defer ctx.Dispose()
			tt.serve.Host = "127.0.0.1"
			tt.serve.Port = -1
			var failedTLSAddress string
			if tt.name == "tls-source-missing" {
				listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
				if err != nil {
					t.Fatal(err)
				}
				failedTLSAddress = listener.Addr().String()
				tt.serve.Port = listener.Addr().(*net.TCPAddr).Port
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, serveErr := ctx.Serve(tt.serve)
			if tt.name == "tls-source-missing" {
				if !errors.Is(serveErr, os.ErrNotExist) {
					t.Fatalf("missing native TLS source lost its filesystem error: %v", serveErr)
				}
				ctx.Dispose()
				listener, err := net.Listen("tcp4", failedTLSAddress)
				if err != nil {
					t.Fatalf("failed native TLS startup retained its listener after Dispose: %v", err)
				}
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if serveErr == nil || !strings.Contains(serveErr.Error(), tt.want) {
				t.Fatalf("native serve validation: error=%v expected=%q", serveErr, tt.want)
			}
		})
	}
}

func TestCatalogAPIContextFallbackAndErrors(t *testing.T) {
	t.Run("fallback", func(t *testing.T) {
		dir := t.TempDir()
		fallback := catalogWriteFile(t, dir, "fallback.html", "<h1>native fallback</h1>")
		server := catalogStartServer(t, api.BuildOptions{AbsWorkingDir: dir}, api.ServeOptions{Fallback: fallback, CORS: api.CORSOptions{Origin: []string{"*"}}})
		request := catalogHTTPRequest(t, server, "GET", "/application/route")
		request.Header.Set("Origin", "https://request.test")
		response := catalogRequest(t, server, request)
		if response.Status != http.StatusOK || response.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("native fallback failed: %+v", response)
		}
		catalogRequireText(t, string(response.Body), "native fallback")
	})
	t.Run("native-build-diagnostics", func(t *testing.T) {
		dir := t.TempDir()
		entry := catalogWriteFile(t, dir, "entry.ts", "export const value = 1;")
		plugin := api.Plugin{Name: "native-build-errors", Setup: func(build api.PluginBuild) {
			build.OnStart(func() (api.OnStartResult, error) {
				var messages []api.Message
				for i := range 8 {
					messages = append(messages, api.Message{Text: fmt.Sprintf("native failure %d", i)})
				}
				return api.OnStartResult{Errors: messages}, nil
			})
		}}
		server := catalogStartServer(t, api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outdir: "out", Plugins: []api.Plugin{plugin}}, api.ServeOptions{})
		response := catalogRequest(t, server, catalogHTTPRequest(t, server, "GET", "/entry.js"))
		if response.Status != http.StatusServiceUnavailable {
			t.Fatalf("failed native build was served: %+v", response)
		}
		catalogRequireText(t, string(response.Body), "native failure 0", "5 out of 8 errors shown")
	})
}

func TestCatalogAPIContextTLS(t *testing.T) {
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const certificateLifetime = time.Hour
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-certificateLifetime), NotAfter: time.Now().Add(certificateLifetime),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := catalogWriteFile(t, dir, "public/cert.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})))
	keyPath := catalogWriteFile(t, dir, "public/key.pem", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})))
	catalogWriteFile(t, dir, "public/index.html", "<h1>native TLS</h1>")
	server := catalogStartServer(t, api.BuildOptions{AbsWorkingDir: dir}, api.ServeOptions{Servedir: "public", Certfile: certPath, Keyfile: keyPath})
	parsed, err := x509.ParseCertificate(certificate)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	server.Client.Transport = transport
	response := catalogRequest(t, server, catalogHTTPRequest(t, server, "GET", "/"))
	if response.Status != http.StatusOK {
		t.Fatalf("verified native TLS request failed: %+v", response)
	}
	catalogRequireText(t, string(response.Body), "native TLS")
	for _, path := range []string{"/cert.pem", "/key.pem"} {
		response := catalogRequest(t, server, catalogHTTPRequest(t, server, "GET", path))
		if response.Status != http.StatusForbidden || bytes.Contains(response.Body, []byte("-----BEGIN")) {
			t.Fatalf("native TLS source file exposed at %s: %+v", path, response)
		}
	}
}

func TestCatalogAPIContextWatch(t *testing.T) {
	dir := t.TempDir()
	entry := catalogWriteFile(t, dir, "entry.ts", `export const value = "native-watch-first";`)
	results := make(chan api.BuildResult, 32)
	plugin := api.Plugin{Name: "native-watch-results", Setup: func(build api.PluginBuild) {
		build.OnEnd(func(result *api.BuildResult) (api.OnEndResult, error) {
			results <- *result
			return api.OnEndResult{}, nil
		})
	}}
	ctx, err := api.Context(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outdir: "out", Plugins: []api.Plugin{plugin}, LogLevel: api.LogLevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Dispose()
	if err := ctx.Watch(api.WatchOptions{Delay: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ctx.Watch(api.WatchOptions{}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("duplicate watch accepted: %v", err)
	}
	wait := func(want string, wantError bool) {
		t.Helper()
		timer := time.NewTimer(catalogHTTPTimeout)
		defer timer.Stop()
		for {
			select {
			case result := <-results:
				if wantError && len(result.Errors) > 0 {
					return
				}
				if !wantError && len(result.Errors) == 0 && len(result.OutputFiles) > 0 && bytes.Contains(result.OutputFiles[0].Contents, []byte(want)) {
					return
				}
			case <-timer.C:
				t.Fatalf("native watch did not observe %q, error=%v", want, wantError)
			}
		}
	}
	wait("native-watch-first", false)
	catalogWriteFile(t, dir, "entry.ts", `export const value = "native-watch-second-longer";`)
	wait("native-watch-second-longer", false)
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	wait("", true)
	catalogWriteFile(t, dir, "entry.ts", `export const value = "native-watch-restored";`)
	wait("native-watch-restored", false)
	ctx.Dispose()
	if result := ctx.Rebuild(); len(result.OutputFiles) != 0 {
		t.Fatal("disposed watcher rebuilt native files")
	}
}

type catalogChange struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Updated []string `json:"updated"`
}

func catalogReadChange(t *testing.T, reader *bufio.Reader) catalogChange {
	t.Helper()
	var event string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		}
		if strings.HasPrefix(line, "data: ") {
			if event != "change" {
				t.Fatalf("native event data lost its event type: %q", event)
			}
			var change catalogChange
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &change); err != nil {
				t.Fatal(err)
			}
			return change
		}
	}
}

func TestCatalogAPIContextEvents(t *testing.T) {
	dir := t.TempDir()
	firstSource := `import asset from "./asset.bin"; export const value = "event-first" + asset;`
	entry := catalogWriteFile(t, dir, "entry.ts", firstSource)
	catalogWriteFile(t, dir, "asset.bin", "native-event-asset")
	entered := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	plugin := api.Plugin{Name: "native-event-subscription", Setup: func(build api.PluginBuild) {
		build.OnStart(func() (api.OnStartResult, error) {
			startOnce.Do(func() { close(entered); <-release })
			return api.OnStartResult{}, nil
		})
	}}
	server := catalogStartServer(t, api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outdir: "out", Bundle: true, Loader: map[string]api.Loader{".bin": api.LoaderFile}, PublicPath: "/public/", Plugins: []api.Plugin{plugin}}, api.ServeOptions{})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	requestContext, cancel := context.WithTimeout(t.Context(), catalogHTTPTimeout)
	defer cancel()
	request := catalogHTTPRequest(t, server, "GET", "/esbuild").WithContext(requestContext)
	request.Header.Set("Accept", "text/event-stream")
	response, err := server.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("native event stream failed: %+v", response.Header)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "retry:") {
		t.Fatalf("native stream startup: %q %v", line, err)
	}
	select {
	case <-server.Requests:
	case <-time.After(catalogHTTPTimeout):
		t.Fatal("event-stream request callback missing")
	}
	// The real initial build cannot publish its graph before subscription.
	select {
	case <-entered:
	case <-time.After(catalogHTTPTimeout):
		t.Fatal("native initial build did not enter its start callback")
	}
	releaseOnce.Do(func() { close(release) })
	first := server.Context.Rebuild()
	catalogRequireSuccess(t, first)
	added := catalogReadChange(t, reader)
	if len(added.Added) != len(first.OutputFiles) || len(added.Removed) != 0 || len(added.Updated) != 0 {
		t.Fatalf("native added event lost graph: %+v", added)
	}
	catalogWriteFile(t, dir, "entry.ts", `export const value = "event-second";`)
	catalogRequireSuccess(t, server.Context.Rebuild())
	changed := catalogReadChange(t, reader)
	if len(changed.Removed) != 1 || len(changed.Updated) != 1 || !strings.HasSuffix(changed.Removed[0], ".bin") || changed.Updated[0] != "/public/entry.js" {
		t.Fatalf("native diff lost removed/updated identity: %+v", changed)
	}
	catalogWriteFile(t, dir, "entry.ts", `export const value = ;`)
	catalogRequireError(t, server.Context.Rebuild().Errors, "Unexpected")
	catalogWriteFile(t, dir, "entry.ts", firstSource)
	catalogRequireSuccess(t, server.Context.Rebuild())
	restored := catalogReadChange(t, reader)
	if len(restored.Added) != 1 || len(restored.Removed) != 0 || len(restored.Updated) != 1 {
		t.Fatalf("failed build changed public event graph: %+v", restored)
	}
	server.Context.Dispose()
	if _, err := io.ReadAll(response.Body); requestContext.Err() != nil || (err != nil && !errors.Is(err, io.ErrUnexpectedEOF)) {
		// Server.Close closes the HTTP/1 stream instead of completing its chunked body.
		t.Fatalf("disposed event stream did not close normally before deadline: %v", err)
	}
}

func TestCatalogAPIContextDisconnect(t *testing.T) {
	const disconnectChild = "CATALOG_API_DISCONNECT_CHILD"
	if os.Getenv(disconnectChild) != "1" {
		catalogRunNativeChild(t, "^TestCatalogAPIContextDisconnect$", disconnectChild)
		return
	}
	dir := t.TempDir()
	entry := catalogWriteFile(t, dir, "entry.ts", `export const value = "native-disconnect-start";`)
	server := catalogStartServer(t, api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outdir: "out"}, api.ServeOptions{})
	const disconnectRounds = 20
	for i := range disconnectRounds {
		request := catalogHTTPRequest(t, server, "GET", "/esbuild")
		request.Header.Set("Accept", "text/event-stream")
		response, err := server.Client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(response.Body)
		line, readErr := reader.ReadString('\n')
		if readErr != nil || !strings.HasPrefix(line, "retry:") || response.StatusCode != http.StatusOK {
			if err := response.Body.Close(); err != nil {
				t.Error(err)
			}
			t.Fatalf("native disconnect subscription failed: status=%d line=%q error=%v", response.StatusCode, line, readErr)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-server.Requests:
			if event.Path != "/esbuild" || event.Status != http.StatusOK {
				t.Fatalf("disconnect request callback lost stream identity: %+v", event)
			}
		case <-time.After(catalogHTTPTimeout):
			t.Fatal("disconnected native stream omitted its request callback")
		}
		marker := fmt.Sprintf("native-disconnect-round-%d", i)
		catalogWriteFile(t, dir, "entry.ts", fmt.Sprintf("export const value = %q;", marker))
		result := server.Context.Rebuild()
		catalogRequireSuccess(t, result)
		catalogRequireText(t, string(result.OutputFiles[0].Contents), marker)
	}
	// Keep a real final stream alive while Dispose must stop and join its writer.
	request := catalogHTTPRequest(t, server, "GET", "/esbuild")
	request.Header.Set("Accept", "text/event-stream")
	response, err := server.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(line, "retry:") {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
		t.Fatalf("final native stream did not start: %q %v", line, err)
	}
	select {
	case <-server.Requests:
	case <-time.After(catalogHTTPTimeout):
		t.Fatal("final native stream callback missing")
	}
	server.Context.Dispose()
	if _, err := io.ReadAll(reader); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("disposed final native stream remained open: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}
