package resolver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/evanw/esbuild/internal/ast"
	"github.com/evanw/esbuild/internal/cache"
	"github.com/evanw/esbuild/internal/config"
	"github.com/evanw/esbuild/internal/fs"
	"github.com/evanw/esbuild/internal/helpers"
	"github.com/evanw/esbuild/internal/logger"
)

func catalogNativeResolver(t *testing.T, files map[string]string, options config.Options) (*Resolver, logger.Log, string) {
	t.Helper()
	root := t.TempDir()
	// A local empty configuration prevents the fixture from inheriting this
	// checkout's TypeScript configuration while resolving native files.
	if _, ok := files["tsconfig.json"]; !ok {
		if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input, err := fs.RealFS(fs.RealFSOptions{AbsWorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if options.ExtensionOrder == nil {
		options.ExtensionOrder = []string{".tsx", ".ts", ".jsx", ".js", ".css", ".json"}
	}
	if options.ExtensionToLoader == nil {
		options.ExtensionToLoader = map[string]config.Loader{
			".tsx": config.LoaderTSX, ".ts": config.LoaderTS, ".jsx": config.LoaderJSX,
			".js": config.LoaderJS, ".css": config.LoaderCSS, ".json": config.LoaderJSON,
		}
	}
	log := logger.NewDeferLog(logger.DeferLogAll, nil)
	log.Level = logger.LevelDebug
	return NewResolver(config.BuildCall, input, log, cache.MakeCacheSet(), &options), log, root
}

func catalogResolvedFile(t *testing.T, result *ResolveResult, root, relative string) {
	t.Helper()
	want := filepath.Join(root, filepath.FromSlash(relative))
	if result == nil || result.PathPair.Primary.Namespace != "file" || result.PathPair.Primary.Text != want || result.PathPair.IsExternal {
		t.Fatalf("resolve = %+v, want native file %s", result, want)
	}
}

func TestCatalogDataURLDecoding(t *testing.T) {
	for _, tt := range []struct {
		name, input, want, errorText string
		mime                         MIMEType
		valid                        bool
	}{
		{"percent_css", "data:text/css;charset=utf-8,a%20%7Bcolor:red%7D", "a {color:red}", "", MIMETypeTextCSS, true},
		{"base64_js", "data:text/javascript;base64,ZXhwb3J0IGRlZmF1bHQgMQ==", "export default 1", "", MIMETypeTextJavaScript, true},
		{"json_plus", "data:application/json,%22a+b%22", "\"a+b\"", "", MIMETypeApplicationJSON, true},
		{"unsupported_image", "data:image/png;base64,YQ==", "a", "", MIMETypeUnsupported, true},
		{"bad_base64", "data:text/javascript;base64,%%%", "", "base64", MIMETypeTextJavaScript, true},
		{"bad_percent", "data:text/css,%ZZ", "", "percent-escaped", MIMETypeTextCSS, true},
		{"no_comma", "data:text/css", "", "", MIMETypeUnsupported, false},
		{"ordinary_path", "./data:text/css,x", "", "", MIMETypeUnsupported, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parsed, ok := ParseDataURL(tt.input)
			if ok != tt.valid {
				t.Fatalf("parse validity = %v", ok)
			}
			if !ok {
				return
			}
			got, err := parsed.DecodeData()
			if tt.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errorText) {
					t.Fatalf("decode error = %v", err)
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("decode = %q, %v; want %q", got, err, tt.want)
			}
			if parsed.DecodeMIMEType() != tt.mime {
				t.Fatalf("decoded media classification = %v", parsed.DecodeMIMEType())
			}
		})
	}
}

func TestCatalogPackageImportsAndExports(t *testing.T) {
	files := map[string]string{
		"package.json":                  `{"name":"app","exports":{"./self":"./src/local.js"},"imports":{"#local":"./src/local.js","#dep":"pkg/feature","#pattern/*":"./src/*.js","#blocked":null}}`,
		"src/local.js":                  "export default 'local'",
		"src/item.js":                   "export default 'item'",
		"node_modules/pkg/package.json": `{"exports":{".":{"import":"./esm.js","require":"./cjs.cjs"},"./feature":"./feature.js","./pattern/*":"./lib/*.js","./blocked":null,"./fallback":[null,"./feature.js"]}}`,
		"node_modules/pkg/esm.js":       "export default 'esm'",
		"node_modules/pkg/cjs.cjs":      "module.exports = 'cjs'",
		"node_modules/pkg/feature.js":   "export default 'feature'",
		"node_modules/pkg/lib/item.js":  "export default 'pattern'",
		"node_modules/pkg/private.js":   "export default 'private'",
	}
	for _, tt := range []struct {
		name, path, want string
		kind             ast.ImportKind
	}{
		{"import_condition", "pkg", "node_modules/pkg/esm.js", ast.ImportStmt},
		{"require_condition", "pkg", "node_modules/pkg/cjs.cjs", ast.ImportRequire},
		{"exact_export", "pkg/feature", "node_modules/pkg/feature.js", ast.ImportStmt},
		{"pattern_export", "pkg/pattern/item", "node_modules/pkg/lib/item.js", ast.ImportStmt},
		{"array_fallback", "pkg/fallback", "node_modules/pkg/feature.js", ast.ImportStmt},
		{"private_unexported", "pkg/private.js", "", ast.ImportStmt},
		{"explicitly_blocked", "pkg/blocked", "", ast.ImportStmt},
		{"missing_export", "pkg/missing", "", ast.ImportStmt},
		{"local_import", "#local", "src/local.js", ast.ImportStmt},
		{"external_package_import", "#dep", "node_modules/pkg/feature.js", ast.ImportStmt},
		{"pattern_import", "#pattern/item", "src/item.js", ast.ImportStmt},
		{"blocked_import", "#blocked", "", ast.ImportStmt},
		{"missing_import", "#missing", "", ast.ImportStmt},
		{"self_reference", "app/self", "src/local.js", ast.ImportStmt},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, log, root := catalogNativeResolver(t, files, config.Options{Platform: config.PlatformNode})
			result, meta := res.Resolve(root, tt.path, tt.kind)
			if tt.want == "" {
				if result != nil {
					t.Fatalf("encapsulated/undefined path unexpectedly resolved: %+v", result)
				}
				source := logger.Source{KeyPath: logger.Path{Text: filepath.Join(root, "entry.js"), Namespace: "file"}, PrettyPaths: logger.PrettyPaths{Abs: "entry.js", Rel: "entry.js"}, Contents: tt.path}
				meta.LogErrorMsg(log, &source, logger.Range{Len: int32(len(tt.path))}, "Cannot resolve "+tt.path, "", nil)
				msgs := log.Done()
				var found bool
				for _, msg := range msgs {
					if msg.Kind == logger.Error && msg.Data.Location != nil && msg.Data.Location.LineText == tt.path && len(msg.Notes) > 0 {
						found = true
					}
				}
				if !found {
					t.Fatalf("resolution rejection lost native explanatory notes: %+v", msgs)
				}
				return
			}
			catalogResolvedFile(t, result, root, tt.want)
			if log.HasErrors() {
				t.Fatalf("valid package resolution emitted errors: %+v", log.Done())
			}
		})
	}
}

func TestCatalogExportTargetsAndFailureDiagnostics(t *testing.T) {
	for _, tt := range []struct{ name, exports, want, note string }{
		{"outside_target", `"../outside.js"`, "", "invalid"},
		{"nested_node_modules", `"./node_modules/hidden.js"`, "", "invalid segment"},
		{"encoded_separator", `"./lib%2Fitem.js"`, "", "invalid"},
		{"unmatched_conditions", `{"browser":"./good.js"}`, "", "conditions"},
		{"missing_exact_file", `"./missing.js"`, "", "not found"},
		{"directory_forbidden", `"./directory"`, "", "directory"},
		{"invalid_numeric_value", `42`, "", "invalid value"},
		{"invalid_array_target_fallback", `["../outside.js","./good.js"]`, "node_modules/target/good.js", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manifest := `{"exports":` + tt.exports + `}`
			res, log, root := catalogNativeResolver(t, map[string]string{
				"node_modules/target/package.json":           manifest,
				"node_modules/target/good.js":                "export default 1",
				"node_modules/target/directory/index.js":     "export default 2",
				"node_modules/target/node_modules/hidden.js": "export default 3",
				"node_modules/outside.js":                    "export default 4",
			}, config.Options{Platform: config.PlatformNode})
			result, meta := res.Resolve(root, "target", ast.ImportStmt)
			if tt.want != "" {
				catalogResolvedFile(t, result, root, tt.want)
				return
			}
			if result != nil {
				t.Fatalf("invalid/unsupported export reached a module: %+v", result)
			}
			var found bool
			for _, note := range meta.notes {
				if strings.Contains(note.Text, tt.note) && note.Location != nil && note.Location.File.Abs == filepath.Join(root, "node_modules", "target", "package.json") && note.Location.LineText == manifest {
					found = true
				}
			}
			if !found {
				t.Fatalf("export rejection lost original manifest/token evidence: notes %+v; logs %+v", meta.notes, log.Done())
			}
		})
	}
}

func TestCatalogAliasesSuffixAndExtensionPriority(t *testing.T) {
	files := map[string]string{
		"package.json":                                              "{}",
		"asset.css":                                                 "body {color: red}",
		"node_modules/replacement/package.json":                     "{}",
		"node_modules/replacement/index.js":                         "export default 'root'",
		"node_modules/replacement/item.js":                          "export default 'item'",
		"node_modules/replacement/item.ts":                          "export default 'typescript'",
		"node_modules/parent/package.json":                          "{}",
		"node_modules/parent/entry.js":                              "",
		"node_modules/parent/node_modules/replacement/package.json": "{}",
		"node_modules/parent/node_modules/replacement/index.js":     "export default 'nested'",
	}
	res, _, root := catalogNativeResolver(t, files, config.Options{Platform: config.PlatformNode, PackageAliases: map[string]string{"old": "replacement", "old/item": "replacement/item"}})
	for _, tt := range []struct{ path, want, alias, suffix string }{
		{"old", "node_modules/replacement/index.js", "replacement", ""},
		{"old/", "node_modules/replacement/index.js", "replacement", ""},
		{"old/item", "node_modules/replacement/item.js", "replacement/item", ""},
		{"./asset.css?#iefix", "asset.css", "", "?#iefix"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			dir := filepath.Join(root, "node_modules", "parent")
			if tt.alias == "" {
				dir = root
			}
			result, meta := res.Resolve(dir, tt.path, ast.ImportStmt)
			catalogResolvedFile(t, result, root, tt.want)
			if meta.ModifiedImportPath != tt.alias || result.PathPair.Primary.IgnoredSuffix != tt.suffix {
				t.Fatalf("alias/suffix identity lost: %+v / %+v", meta, result)
			}
		})
	}
	if result, meta := res.Resolve(root, "older", ast.ImportStmt); result != nil || meta.ModifiedImportPath != "" {
		t.Fatalf("alias crossed package-name boundary: %+v / %+v", result, meta)
	}
}

func TestCatalogBrowserRemappingUsesEvaluatedPlatform(t *testing.T) {
	files := map[string]string{
		"package.json":                          `{"browser":{"./server.js":"./browser.js","./ignore.js":false,"pkg":"replacement"}}`,
		"server.js":                             "export default 'server'",
		"browser.js":                            "export default 'browser'",
		"ignore.js":                             "export default 'ignored only on browser'",
		"node_modules/pkg/package.json":         `{"main":"./index.js"}`,
		"node_modules/pkg/index.js":             "export default 'original'",
		"node_modules/replacement/package.json": `{"main":"./index.js","sideEffects":false}`,
		"node_modules/replacement/index.js":     "export default 'replacement'",
		"node_modules/index-pkg/package.json":   `{"browser":{"./index":"./fallback"}}`,
		"node_modules/index-pkg/index.js":       "export default 'index'",
		"node_modules/index-pkg/fallback.js":    "export default 'fallback'",
	}
	for _, tt := range []struct {
		name, path, want         string
		platform                 config.Platform
		disabled, sideEffectFree bool
	}{
		{"browser_file", "./server.js", "browser.js", config.PlatformBrowser, false, false},
		{"node_file", "./server.js", "server.js", config.PlatformNode, false, false},
		{"browser_disabled", "./ignore.js", "ignore.js", config.PlatformBrowser, true, false},
		{"node_enabled", "./ignore.js", "ignore.js", config.PlatformNode, false, false},
		{"browser_package", "pkg", "node_modules/replacement/index.js", config.PlatformBrowser, false, true},
		{"node_package", "pkg", "node_modules/pkg/index.js", config.PlatformNode, false, false},
		{"browser_index", "index-pkg", "node_modules/index-pkg/fallback.js", config.PlatformBrowser, false, false},
		{"node_index", "index-pkg", "node_modules/index-pkg/index.js", config.PlatformNode, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, log, root := catalogNativeResolver(t, files, config.Options{Platform: tt.platform})
			result, _ := res.Resolve(root, tt.path, ast.ImportStmt)
			catalogResolvedFile(t, result, root, tt.want)
			if (result.PathPair.Primary.Flags&logger.PathDisabled != 0) != tt.disabled || (result.PrimarySideEffectsData != nil) != tt.sideEffectFree {
				t.Fatalf("browser/side-effect metadata = %+v", result)
			}
			if log.HasErrors() {
				t.Fatalf("browser remapping emitted errors: %+v", log.Done())
			}
		})
	}
}

func TestCatalogExternalAndVirtualResolution(t *testing.T) {
	// The real API validator expands package externals into both an exact
	// package match and a package-subpath wildcard before creating a resolver.
	externals := config.ExternalMatchers{Exact: map[string]bool{"external": true}, Patterns: []config.WildcardPattern{{Prefix: "external/"}}}
	res, _, root := catalogNativeResolver(t, nil, config.Options{Platform: config.PlatformNode, ExternalSettings: config.ExternalSettings{PreResolve: externals}})
	for _, tt := range []struct {
		path, namespace string
		kind            ast.ImportKind
		external        bool
	}{
		{"external", "", ast.ImportStmt, true},
		{"external/subpath", "", ast.ImportStmt, true},
		{"https://example.invalid/module.js", "", ast.ImportStmt, true},
		{"http://example.invalid/module.js", "", ast.ImportStmt, true},
		{"//example.invalid/image.png", "", ast.ImportURL, true},
		{"#filter", "", ast.ImportURL, true},
		{"node:fs", "", ast.ImportStmt, true},
		{"data:text/javascript,export%20default%201", "dataurl", ast.ImportStmt, false},
		{"data:image/png;base64,YQ==", "", ast.ImportURL, true},
	} {
		t.Run(tt.path, func(t *testing.T) {
			result, _ := res.Resolve(root, tt.path, tt.kind)
			if result == nil || result.PathPair.Primary.Text != tt.path || result.PathPair.Primary.Namespace != tt.namespace || result.PathPair.IsExternal != tt.external {
				t.Fatalf("external/virtual resolution = %+v", result)
			}
		})
	}
	for _, tt := range []struct{ dir, path string }{{"", "./local.js"}, {root, "./*.js"}, {filepath.Join(root, "missing"), "./local.js"}} {
		if result, _ := res.Resolve(tt.dir, tt.path, ast.ImportStmt); result != nil {
			t.Fatalf("invalid resolution context produced a file: %+v", result)
		}
	}
}

func TestCatalogTSConfigInheritanceAndPaths(t *testing.T) {
	files := map[string]string{
		"base-a.json":   `{"compilerOptions":{"strict":true,"experimentalDecorators":true,"target":"es5","jsxFactory":"UI.createElement","baseUrl":".","paths":{"@local/*":["missing/*","src/*"]}}}`,
		"base-b.json":   `{"compilerOptions":{"target":"es2022","preserveValueImports":true,"useDefineForClassFields":true}}`,
		"tsconfig.json": `{"extends":["./base-a.json","./base-b.json"],"compilerOptions":{"alwaysStrict":false,"useDefineForClassFields":false,"jsx":"react-jsx","jsxImportSource":"preact"}}`,
		"src/item.ts":   "export default 1",
	}
	res, log, root := catalogNativeResolver(t, files, config.Options{Platform: config.PlatformNode})
	result, _ := res.Resolve(root, "@local/item", ast.ImportStmt)
	catalogResolvedFile(t, result, root, "src/item.ts")
	if result.TSConfig == nil || result.TSConfig.ExperimentalDecorators != config.True || result.TSConfig.PreserveValueImports != config.True || result.TSConfig.UseDefineForClassFields != config.False || result.TSConfig.Target != config.TSTargetAtOrAboveES2022 {
		t.Fatalf("resolved source lost inherited/overridden compiler semantics: %+v", result.TSConfig)
	}
	if result.TSAlwaysStrict == nil || result.TSAlwaysStrict.Value || result.TSAlwaysStrict.Name != "alwaysStrict" {
		t.Fatalf("explicit alwaysStrict did not override inherited strict: %+v", result.TSAlwaysStrict)
	}
	if log.HasErrors() {
		t.Fatalf("valid inherited configuration failed: %+v", log.Done())
	}
}

func TestCatalogTSConfigDiagnostics(t *testing.T) {
	for _, tt := range []struct{ name, contents, message string }{
		{"target", `{"compilerOptions":{"target":"not-an-engine"}}`, "Unrecognized target environment"},
		{"jsx_member", `{"compilerOptions":{"jsxFactory":"UI..create"}}`, "Invalid JSX member expression"},
		{"pattern", `{"compilerOptions":{"paths":{"bad**":["./src/*"]}}}`, "at most one"},
		{"substitution", `{"compilerOptions":{"paths":{"alias":["./src/**"]}}}`, "at most one"},
		{"paths_array", `{"compilerOptions":{"paths":{"alias":"./src/value"}}}`, "should be an array"},
		{"imports_mode", `{"compilerOptions":{"importsNotUsedAsValues":"invalid"}}`, "Invalid value"},
		{"top_level", `{"useDefineForClassFields":false}`, "nested inside"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, log, root := catalogNativeResolver(t, map[string]string{"tsconfig.json": tt.contents, "entry.ts": "export default 1"}, config.Options{})
			result, _ := res.Resolve(root, "./entry.ts", ast.ImportStmt)
			catalogResolvedFile(t, result, root, "entry.ts")
			var found bool
			for _, msg := range log.Done() {
				if msg.Kind == logger.Warning && strings.Contains(msg.Data.Text, tt.message) && msg.Data.Location != nil && msg.Data.Location.File.Abs == filepath.Join(root, "tsconfig.json") && msg.Data.Location.LineText == tt.contents {
					found = true
				}
			}
			if !found {
				t.Fatalf("invalid configuration lost source-bound warning: %+v", log.Done())
			}
		})
	}
}

func TestCatalogNativeGlobResolution(t *testing.T) {
	res, _, root := catalogNativeResolver(t, map[string]string{
		"files/a.js":        "export default 'a'",
		"files/nested/b.js": "export default 'b'",
		"files/style.css":   "body {}",
	}, config.Options{ExternalSettings: config.ExternalSettings{PreResolve: config.ExternalMatchers{Patterns: []config.WildcardPattern{{Prefix: "./files/a", Suffix: ".js"}}}}})
	// A real directory link back to its parent must not turn a recursive glob
	// into an unbounded traversal or duplicate the same fixture modules.
	if err := os.Symlink(filepath.Join(root, "files"), filepath.Join(root, "files", "loop")); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, pattern, want string
		kind                ast.ImportKind
		wantNil, wantEmpty  bool
	}{
		{"one_segment", "./files/*.js", "./files/a.js", ast.ImportStmt, false, false},
		{"recursive", "./files/**/*.js", "./files/a.js,./files/nested/b.js", ast.ImportStmt, false, false},
		{"entry_point_prefix", "files/*.js", "./files/a.js", ast.ImportEntryPoint, false, false},
		{"package_glob_rejected", "files/*.js", "", ast.ImportStmt, true, false},
		{"missing_directory", "./missing/*.js", "", ast.ImportStmt, true, false},
		{"no_matches", "./files/*.ts", "", ast.ImportStmt, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			results, warning := res.ResolveGlob(root, helpers.ParseGlobPattern(tt.pattern), tt.kind, tt.pattern)
			if (results == nil) != tt.wantNil {
				t.Fatalf("glob validity = %v; warning %+v", results != nil, warning)
			}
			var paths []string
			for key, result := range results {
				paths = append(paths, key)
				if key == "./files/a.js" && tt.kind != ast.ImportEntryPoint {
					if !result.PathPair.IsExternal || result.PathPair.Primary.Text != key {
						t.Fatalf("matched external module became a native input: %+v", result)
					}
				} else {
					catalogResolvedFile(t, &result, root, strings.TrimPrefix(key, "./"))
				}
			}
			slices.Sort(paths)
			if strings.Join(paths, ",") != tt.want {
				t.Fatalf("native glob matched %q, want %q", paths, tt.want)
			}
			if tt.wantEmpty {
				if warning == nil || warning.Kind != logger.Warning || warning.ID != logger.MsgID_Bundler_EmptyGlob || !strings.Contains(warning.Data.Text, tt.pattern) {
					t.Fatalf("valid empty glob lost its warning: %+v", warning)
				}
			} else if warning != nil {
				t.Fatalf("unexpected glob warning: %+v", warning)
			}
		})
	}
	if results, warning := res.ResolveGlob(root, nil, ast.ImportStmt, "empty"); results != nil || warning != nil {
		t.Fatalf("absent glob is not a valid empty query: %+v / %+v", results, warning)
	}
}
