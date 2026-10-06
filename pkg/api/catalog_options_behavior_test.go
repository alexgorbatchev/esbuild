package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evanw/esbuild/pkg/api"
)

func TestCatalogAPITargetSemantics(t *testing.T) {
	targets := []api.Target{api.ES5, api.ES2015, api.ES2016, api.ES2017, api.ES2018, api.ES2019, api.ES2020, api.ES2021, api.ES2022, api.ES2023, api.ES2024, api.ES2025, api.ESNext}
	for _, target := range targets {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			result := api.Transform(`var value = () => "native-target";`, api.TransformOptions{Target: target, LogLevel: api.LogLevelSilent})
			if len(result.Errors) != 0 {
				t.Fatal(result.Errors)
			}
			catalogRequireText(t, string(result.Code), "native-target")
			if target == api.ES5 {
				if bytes.Contains(result.Code, []byte("=>")) {
					t.Fatal("native ES5 target retained arrow syntax")
				}
				catalogRequireText(t, string(result.Code), "function")
			} else {
				catalogRequireText(t, string(result.Code), "=>")
			}
		})
	}
	engines := []struct {
		name  api.EngineName
		label string
	}{{api.EngineChrome, "chrome"}, {api.EngineDeno, "deno"}, {api.EngineEdge, "edge"}, {api.EngineFirefox, "firefox"}, {api.EngineHermes, "hermes"}, {api.EngineIE, "ie"}, {api.EngineIOS, "ios"}, {api.EngineNode, "node"}, {api.EngineOpera, "opera"}, {api.EngineRhino, "rhino"}, {api.EngineSafari, "safari"}}
	for _, engine := range engines {
		t.Run("engine-"+engine.label, func(t *testing.T) {
			input := fmt.Sprintf("var proof = %q;", t.Name())
			options := api.TransformOptions{Engines: []api.Engine{{Name: engine.name, Version: "999.2.3-preview.1"}}, LogLevel: api.LogLevelSilent}
			result := api.Transform(input, options)
			if len(result.Errors) != 0 {
				t.Fatal(result.Errors)
			}
			catalogRequireText(t, string(result.Code), t.Name())
			options.Target = api.ES5
			diagnostic := api.Transform(`await Promise.resolve("native-engine-target");`, options)
			message := catalogRequireError(t, diagnostic.Errors, "Top-level await")
			catalogRequireText(t, message.Text, "es5", engine.label+"999.2.3-preview.1")
		})
	}
	for _, supported := range []bool{false, true} {
		result := api.Transform(`var value = () => "native-supported";`, api.TransformOptions{Target: api.ES5, Supported: map[string]bool{"arrow": supported}, LogLevel: api.LogLevelSilent})
		if len(result.Errors) != 0 || bytes.Contains(result.Code, []byte("=>")) != supported {
			t.Fatalf("native feature override did not control lowering: supported=%v errors=%+v code=%s", supported, result.Errors, result.Code)
		}
	}
	for _, supported := range []bool{false, true} {
		result := api.Transform(`.native { color: #ff000080; }`, api.TransformOptions{Loader: api.LoaderCSS, Supported: map[string]bool{"hex-rgba": supported}, LogLevel: api.LogLevelSilent})
		if len(result.Errors) != 0 || bytes.Contains(result.Code, []byte("rgba(")) == supported {
			t.Fatalf("native CSS feature override did not control lowering: supported=%v errors=%+v code=%s", supported, result.Errors, result.Code)
		}
	}
}

func TestCatalogAPIOutputOptions(t *testing.T) {
	t.Run("charset", func(t *testing.T) {
		for _, charset := range []api.Charset{api.CharsetASCII, api.CharsetUTF8} {
			result := api.Transform(`export const text = "λ";`, api.TransformOptions{Charset: charset, LogLevel: api.LogLevelSilent})
			if len(result.Errors) != 0 || bytes.Contains(result.Code, []byte("λ")) != (charset == api.CharsetUTF8) {
				t.Fatalf("native charset did not control escaping: %+v code=%s", result.Errors, result.Code)
			}
		}
	})
	t.Run("tree-shaking", func(t *testing.T) {
		for _, shaking := range []api.TreeShaking{api.TreeShakingFalse, api.TreeShakingTrue} {
			result := api.Transform(`const unusedNative = 1; export const retained = 2;`, api.TransformOptions{TreeShaking: shaking, LogLevel: api.LogLevelSilent})
			if len(result.Errors) != 0 || bytes.Contains(result.Code, []byte("unusedNative")) != (shaking == api.TreeShakingFalse) {
				t.Fatalf("native tree shaking did not control unused declaration: %+v code=%s", result.Errors, result.Code)
			}
			catalogRequireText(t, string(result.Code), "retained")
		}
	})
	t.Run("css-banners", func(t *testing.T) {
		result := api.Transform(`.native { color: red; }`, api.TransformOptions{Loader: api.LoaderCSS, Banner: "/* native CSS banner */", Footer: "/* native CSS footer */", LogLevel: api.LogLevelSilent})
		if len(result.Errors) != 0 {
			t.Fatal(result.Errors)
		}
		if !bytes.HasPrefix(result.Code, []byte("/* native CSS banner */")) || !bytes.Contains(result.Code, []byte("color: red")) || !bytes.HasSuffix(bytes.TrimSpace(result.Code), []byte("/* native CSS footer */")) {
			t.Fatalf("native CSS output lost ordered banners: %s", result.Code)
		}
	})
	t.Run("platform-default-format", func(t *testing.T) {
		for _, tt := range []struct {
			platform api.Platform
			want     string
		}{{api.PlatformBrowser, "(() => {"}, {api.PlatformNode, "module.exports"}, {api.PlatformNeutral, "export {"}} {
			result := api.Build(api.BuildOptions{AbsWorkingDir: t.TempDir(), Stdin: &api.StdinOptions{Contents: `export const proof = "native-platform";`, Loader: api.LoaderJS}, Bundle: true, Platform: tt.platform, LogLevel: api.LogLevelSilent})
			catalogRequireSuccess(t, result)
			catalogRequireText(t, string(result.OutputFiles[0].Contents), tt.want, "native-platform")
		}
	})
}

func TestCatalogAPIOutputArtifacts(t *testing.T) {
	const input = `/*! native artifact license */ export const proof: string = "native-artifact";`
	for _, mode := range []api.SourceMap{api.SourceMapLinked, api.SourceMapExternal, api.SourceMapInlineAndExternal} {
		t.Run(fmt.Sprintf("map-%d", mode), func(t *testing.T) {
			dir := t.TempDir()
			result := api.Build(api.BuildOptions{AbsWorkingDir: dir, Stdin: &api.StdinOptions{Contents: input, Sourcefile: "input.ts", Loader: api.LoaderTS}, Outfile: "out.js", Sourcemap: mode, SourceRoot: "https://source.test/", SourcesContent: api.SourcesContentExclude, LogLevel: api.LogLevelSilent})
			catalogRequireSuccess(t, result)
			code := catalogOutput(t, result, "out.js")
			var sourceMap struct {
				Sources        []string `json:"sources"`
				SourcesContent []string `json:"sourcesContent"`
				SourceRoot     string   `json:"sourceRoot"`
				Mappings       string   `json:"mappings"`
			}
			if err := json.Unmarshal(catalogOutput(t, result, "out.js.map").Contents, &sourceMap); err != nil {
				t.Fatal(err)
			}
			if len(sourceMap.Sources) != 1 || sourceMap.Sources[0] != "input.ts" || len(sourceMap.SourcesContent) != 0 || sourceMap.SourceRoot != "https://source.test/" || sourceMap.Mappings == "" {
				t.Fatalf("native external map lost source identity/options: %+v", sourceMap)
			}
			if mode == api.SourceMapLinked {
				catalogRequireText(t, string(code.Contents), "sourceMappingURL=out.js.map")
			} else if mode == api.SourceMapInlineAndExternal {
				catalogRequireText(t, string(code.Contents), "sourceMappingURL=data:application/json;base64,")
			} else if bytes.Contains(code.Contents, []byte("sourceMappingURL=")) {
				t.Fatal("unlinked native map added a source-map reference")
			}
		})
	}
	for _, mode := range []api.LegalComments{api.LegalCommentsNone, api.LegalCommentsEndOfFile, api.LegalCommentsLinked} {
		t.Run(fmt.Sprintf("legal-%d", mode), func(t *testing.T) {
			result := api.Build(api.BuildOptions{AbsWorkingDir: t.TempDir(), Stdin: &api.StdinOptions{Contents: input, Sourcefile: "input.ts", Loader: api.LoaderTS}, Outfile: "out.js", LegalComments: mode, LogLevel: api.LogLevelSilent})
			catalogRequireSuccess(t, result)
			code := catalogOutput(t, result, "out.js")
			if mode == api.LegalCommentsLinked {
				catalogRequireText(t, string(code.Contents), "For license information please see out.js.LEGAL.txt")
				catalogRequireText(t, string(catalogOutput(t, result, "out.js.LEGAL.txt").Contents), "native artifact license")
			} else if bytes.Contains(code.Contents, []byte("native artifact license")) != (mode == api.LegalCommentsEndOfFile) {
				t.Fatalf("native legal-comment policy lost placement: %s", code.Contents)
			}
		})
	}
}

func TestCatalogAPINativeResolutionOptions(t *testing.T) {
	dir := t.TempDir()
	catalogWriteFile(t, dir, "node_modules/native-package/package.json", `{"main":"index.js"}`)
	catalogWriteFile(t, dir, "node_modules/native-package/index.js", `export const proof = "native-bundled-package";`)
	entry := catalogWriteFile(t, dir, "entry.js", `import { proof } from "native-package"; console.log(proof);`)
	for _, packages := range []api.Packages{api.PackagesBundle, api.PackagesExternal} {
		result := api.Build(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outfile: "out.js", Bundle: true, Format: api.FormatESModule, Packages: packages, LogLevel: api.LogLevelSilent})
		catalogRequireSuccess(t, result)
		code := result.OutputFiles[0].Contents
		if packages == api.PackagesExternal {
			catalogRequireText(t, string(code), `from "native-package"`)
			if bytes.Contains(code, []byte("native-bundled-package")) {
				t.Fatal("external package was bundled")
			}
		} else {
			catalogRequireText(t, string(code), "native-bundled-package")
		}
	}
	catalogWriteFile(t, dir, "vendor/native-node-path/index.js", `export const value = "native-node-path";`)
	entry = catalogWriteFile(t, dir, "advanced.ts", `import { value } from "native-node-path"; export default value;`)
	result := api.Build(api.BuildOptions{AbsWorkingDir: dir, EntryPointsAdvanced: []api.EntryPoint{{InputPath: entry, OutputPath: "renamed"}}, Outdir: "out", EntryNames: "[dir]/[name]-[hash].[ext]-[literal]", Bundle: true, Format: api.FormatESModule, NodePaths: []string{"vendor"}, MainFields: []string{"main"}, AbsPaths: api.CodeAbsPath | api.LogAbsPath | api.MetafileAbsPath, Metafile: true, LogLevel: api.LogLevelSilent})
	catalogRequireSuccess(t, result)
	if !strings.HasPrefix(filepath.Base(result.OutputFiles[0].Path), "renamed-") || !strings.HasSuffix(result.OutputFiles[0].Path, ".js-[literal].js") {
		t.Fatalf("native advanced entry naming lost templates: %s", result.OutputFiles[0].Path)
	}
	catalogRequireText(t, string(result.OutputFiles[0].Contents), "native-node-path", dir)
	catalogRequireText(t, result.Metafile, dir)
	for _, external := range []string{"native-*", "./node_modules/native-package/*", "./node_modules/native-package/index.js"} {
		result := api.Build(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{filepath.Join(dir, "entry.js")}, Outdir: "out", Bundle: true, Format: api.FormatESModule, External: []string{external}, LogLevel: api.LogLevelSilent})
		catalogRequireSuccess(t, result)
		if bytes.Contains(result.OutputFiles[0].Contents, []byte("native-bundled-package")) {
			t.Fatalf("native pre/post-resolution external matcher bundled excluded bytes: %q", external)
		}
	}
}

func TestCatalogAPINativeWriteLifecycle(t *testing.T) {
	dir := t.TempDir()
	entry := catalogWriteFile(t, dir, "entry.js", `import asset from "./asset.bin"; export default asset;`)
	catalogWriteFile(t, dir, "asset.bin", "native-written-asset")
	ctx, err := api.Context(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outdir: "out", Bundle: true, Format: api.FormatESModule, Loader: map[string]api.Loader{".bin": api.LoaderFile}, Write: true, LogLevel: api.LogLevelSilent})
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Dispose()
	first := ctx.Rebuild()
	catalogRequireSuccess(t, first)
	output := catalogOutput(t, first, "entry.js")
	asset := catalogOutput(t, first, ".bin")
	const preservedTimestamp = 123456789
	oldTime := time.Unix(preservedTimestamp, 0)
	if err := os.Chtimes(output.Path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	catalogRequireSuccess(t, ctx.Rebuild())
	info, statErr := os.Stat(output.Path)
	if statErr != nil || !info.ModTime().Equal(oldTime) {
		t.Fatalf("native unchanged rebuild rewrote output: info=%+v error=%v", info, statErr)
	}
	if err := os.WriteFile(output.Path, []byte("tampered generated bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	catalogRequireSuccess(t, ctx.Rebuild())
	actual, readErr := os.ReadFile(output.Path)
	if readErr != nil || !bytes.Equal(actual, output.Contents) {
		t.Fatalf("native unchanged-hash rebuild did not restore changed disk bytes: %v", readErr)
	}
	catalogWriteFile(t, dir, "entry.js", `export default "native-rewritten";`)
	catalogRequireSuccess(t, ctx.Rebuild())
	if _, err := os.Stat(asset.Path); !os.IsNotExist(err) {
		t.Fatalf("native rebuild retained obsolete asset: %v", err)
	}
	for _, tt := range []struct{ name, blockedPath, want string }{
		{"directory-parent", "blocked", "Failed to create output directory"},
		{"output-leaf", "blocked/out.js", "Failed to write to output file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.name == "directory-parent" {
				catalogWriteFile(t, dir, tt.blockedPath, "native blocking ancestor")
			} else if err := os.MkdirAll(filepath.Join(dir, tt.blockedPath), 0700); err != nil {
				t.Fatal(err)
			}
			result := api.Build(api.BuildOptions{AbsWorkingDir: dir, Stdin: &api.StdinOptions{Contents: `export const proof = "native-write-failure";`, Loader: api.LoaderJS}, Outfile: "blocked/out.js", Write: true, LogLevel: api.LogLevelSilent})
			catalogRequireError(t, result.Errors, tt.want)
		})
	}
}

func catalogCaptureStderr(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = writer
	var captured bytes.Buffer
	finished := make(chan error, 1)
	go func() {
		_, readErr := io.Copy(&captured, reader)
		finished <- readErr
	}()
	func() {
		defer func() {
			os.Stderr = original
			if err := writer.Close(); err != nil {
				t.Error(err)
			}
			if err := <-finished; err != nil {
				t.Error(err)
			}
			if err := reader.Close(); err != nil {
				t.Error(err)
			}
		}()
		run()
	}()
	return captured.String()
}

func TestCatalogAPINativeLogging(t *testing.T) {
	for _, yarn := range []bool{false, true} {
		t.Run(fmt.Sprintf("yarn-%v", yarn), func(t *testing.T) {
			if yarn {
				t.Setenv("npm_config_user_agent", "yarn/1.22")
			} else {
				t.Setenv("npm_config_user_agent", "native-catalog-test")
			}
			dir := t.TempDir()
			catalogWriteFile(t, dir, "small.txt", "native small asset")
			catalogWriteFile(t, dir, "large.bin", strings.Repeat("native-large-asset", 70000))
			entry := catalogWriteFile(t, dir, "entry.js", `import small from "./small.txt"; import large from "./large.bin"; console.log(small, large);`)
			log := catalogCaptureStderr(t, func() {
				result := api.Build(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outdir: "out", Loader: map[string]api.Loader{".txt": api.LoaderFile, ".bin": api.LoaderFile}, Bundle: true, Write: true, Sourcemap: api.SourceMapLinked, Color: api.ColorAlways, LogLevel: api.LogLevelInfo})
				catalogRequireSuccess(t, result)
			})
			catalogRequireText(t, log, "entry.js", ".map", "small-", "large-", "mb", "\x1b[")
			if strings.Contains(log, "Done in") == yarn {
				t.Fatalf("native Yarn timing policy was not applied: %s", log)
			}
		})
	}
}

func TestCatalogAPINativeServeLogging(t *testing.T) {
	dir := t.TempDir()
	entry := catalogWriteFile(t, dir, "entry.js", `export const proof = "native-server-logging";`)
	requests := make(chan api.ServeOnRequestArgs, 1)
	log := catalogCaptureStderr(t, func() {
		ctx, err := api.Context(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outdir: "out", LogLevel: api.LogLevelVerbose, Color: api.ColorNever})
		if err != nil {
			t.Fatal(err)
		}
		defer ctx.Dispose()
		served, serveErr := ctx.Serve(api.ServeOptions{Host: "localhost", Port: -1, OnRequest: func(args api.ServeOnRequestArgs) { requests <- args }})
		if serveErr != nil {
			t.Fatal(serveErr)
		}
		client := &http.Client{Timeout: catalogHTTPTimeout, Transport: &http.Transport{}}
		defer client.CloseIdleConnections()
		response, requestErr := client.Get("http://" + net.JoinHostPort("localhost", fmt.Sprint(served.Port)) + "/entry.js")
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("native logged server failed: status=%d read=%v close=%v", response.StatusCode, readErr, closeErr)
		}
		catalogRequireText(t, string(body), "native-server-logging")
		select {
		case event := <-requests:
			if event.Status != http.StatusOK || event.Path != "/entry.js" {
				t.Fatalf("logged native request lost identity: %+v", event)
			}
		case <-time.After(catalogHTTPTimeout):
			t.Fatal("logged native request callback omitted")
		}
	})
	catalogRequireText(t, log, "Local:", "Network:", "localhost:")
	if strings.Contains(log, "\x1b[") {
		t.Fatalf("native ColorNever emitted terminal escapes: %s", log)
	}
}
