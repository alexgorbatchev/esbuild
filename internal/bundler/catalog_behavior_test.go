package bundler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanw/esbuild/internal/ast"
	"github.com/evanw/esbuild/internal/cache"
	"github.com/evanw/esbuild/internal/config"
	"github.com/evanw/esbuild/internal/fs"
	"github.com/evanw/esbuild/internal/graph"
	"github.com/evanw/esbuild/internal/helpers"
	"github.com/evanw/esbuild/internal/logger"
	"github.com/evanw/esbuild/internal/resolver"
)

func catalogBundlerFS(t *testing.T, files map[string]string) (fs.FS, string) {
	t.Helper()
	root := t.TempDir()
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
	return input, root
}

func TestCatalogNativeSourceMapComments(t *testing.T) {
	const contents = `{"version":3,"sources":["original.ts"],"mappings":"AAAA"}`
	input, root := catalogBundlerFS(t, map[string]string{"maps/original map.json": contents})
	mapPath := filepath.Join(root, "maps", "original map.json")
	for _, tt := range []struct {
		name, comment, errorText string
		noResolveDir             bool
		wantContents             bool
		wantInline               bool
		kind                     logger.MsgKind
	}{
		{"relative_encoded", "maps/original%20map.json", "", false, true, false, 0},
		{"absolute_file", helpers.FileURLFromFilePath(mapPath).String(), "", false, true, false, 0},
		{"inline_percent", "data:application/json,%7B%22version%22:3%7D", "", false, true, true, 0},
		{"inline_base64", "data:application/json;base64,eyJ2ZXJzaW9uIjozfQ==", "", false, true, true, 0},
		{"bad_inline_base64", "data:application/json;base64,%%%", "base64", false, false, false, logger.Warning},
		{"bad_inline_escape", "data:application/json,%ZZ", "percent-escaped", false, false, false, logger.Warning},
		{"bad_url_escape", "maps/%ZZ.json", "invalid URL escape", false, false, false, logger.Warning},
		{"unsupported_https", "https://example.invalid/map.json", "Unsupported URL scheme", false, false, false, logger.Debug},
		{"foreign_file_host", "file://remote.invalid/map.json", "Unsupported host", false, false, false, logger.Warning},
		{"no_resolve_directory", "maps/original%20map.json", "without a resolve directory", true, false, false, logger.Debug},
		{"missing_file", "maps/missing.json", "Cannot read file", false, false, false, logger.Debug},
	} {
		t.Run(tt.name, func(t *testing.T) {
			log := logger.NewDeferLog(logger.DeferLogAll, nil)
			source := logger.Source{KeyPath: logger.Path{Text: filepath.Join(root, "entry.js"), Namespace: "file"}, PrettyPaths: logger.PrettyPaths{Abs: "entry.js", Rel: "entry.js"}, Contents: "//# sourceMappingURL=" + tt.comment}
			tracker := logger.MakeLineColumnTracker(&source)
			comment := logger.Span{Text: tt.comment, Range: logger.Range{Loc: logger.Loc{Start: int32(strings.Index(source.Contents, tt.comment))}, Len: int32(len(tt.comment))}}
			dir := root
			if tt.noResolveDir {
				dir = ""
			}
			caches := cache.MakeCacheSet()
			path, bytes := extractSourceMapFromComment(log, input, &caches.FSCache, &source, &tracker, comment, dir, logger.RelPath)
			if (bytes != nil) != tt.wantContents {
				t.Fatalf("source map read = %v, path %+v, diagnostics %+v", bytes, path, log.Done())
			}
			if bytes != nil {
				want := contents
				if tt.wantInline {
					want = `{"version":3}`
					if path != (logger.Path{Text: source.KeyPath.Text, Namespace: "file", IgnoredSuffix: "#sourceMappingURL"}) {
						t.Fatalf("inline map lost its originating module identity: %+v", path)
					}
				} else if path != (logger.Path{Text: mapPath, Namespace: "file"}) {
					t.Fatalf("native map path = %+v, want %s", path, mapPath)
				}
				if *bytes != want {
					t.Fatalf("source map bytes changed: %q", *bytes)
				}
			}
			msgs := log.Done()
			if tt.errorText == "" {
				if len(msgs) != 0 {
					t.Fatalf("valid map emitted diagnostics: %+v", msgs)
				}
			} else if len(msgs) != 1 || msgs[0].Kind != tt.kind || !strings.Contains(msgs[0].Data.Text, tt.errorText) || msgs[0].Data.Location == nil || msgs[0].Data.Location.LineText != source.Contents {
				t.Fatalf("rejected map lost its source-bound diagnostic: %+v", msgs)
			}
		})
	}
}

func TestCatalogOutputNamesRespectNativeOutbase(t *testing.T) {
	input, root := catalogBundlerFS(t, nil)
	options := config.Options{AbsOutputBase: filepath.Join(root, "src")}
	for _, tt := range []struct {
		name, path, namespace, custom, dir, base string
		avoidIndex                               bool
	}{
		{"native_entry", filepath.Join(root, "src", "feature", "entry.ts"), "file", "", "/feature", "entry", false},
		{"native_index", filepath.Join(root, "src", "feature", "index.js"), "file", "", "/", "feature", true},
		{"outside_outbase", filepath.Join(root, "other", "entry.ts"), "file", "", "/_.._/other", "entry", false},
		{"custom_output", filepath.Join(root, "src", "entry.ts"), "file", "chosen/output.name", "/chosen", "output.name", false},
		{"virtual_sanitized", "plugin-dir/a?b.js", "plugin", "", "/", "a_b", false},
		{"virtual_index", "plugin-dir/index.js", "plugin", "", "/", "plugin-dir", true},
		{"virtual_empty_after_sanitize", "plugin-dir/\x00<>.js", "plugin", "", "/", "_", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			file := graph.InputFile{Source: logger.Source{KeyPath: logger.Path{Text: tt.path, Namespace: tt.namespace}}}
			dir, base := PathRelativeToOutbase(&file, &options, input, tt.avoidIndex, tt.custom)
			if dir != tt.dir || base != tt.base {
				t.Fatalf("output name = %q/%q, want %q/%q", dir, base, tt.dir, tt.base)
			}
		})
	}
}

func TestCatalogResolutionFailureHints(t *testing.T) {
	input, root := catalogBundlerFS(t, map[string]string{"tsconfig.json": "{}", "local.js": "export default 1"})
	log := logger.NewDeferLog(logger.DeferLogAll, nil)
	options := config.Options{ExtensionOrder: []string{".js"}, ExtensionToLoader: map[string]config.Loader{".js": config.LoaderJS}}
	res := resolver.NewResolver(config.BuildCall, input, log, cache.MakeCacheSet(), &options)
	for _, tt := range []struct {
		name, path, plugin, alias, hint, suggestion string
		kind                                        ast.ImportKind
		noResolveDir                                bool
	}{
		{"local_relative_suggestion", "local.js", "", "", "leading \"./\"", `"./local.js"`, ast.ImportStmt, false},
		{"require_recovery", "missing-package", "", "", "try/catch", "", ast.ImportRequire, false},
		{"dynamic_recovery", "missing-package", "", "", ".catch()", "", ast.ImportDynamic, false},
		{"node_builtin", "node:fs", "", "", "built into node", "", ast.ImportStmt, false},
		{"plugin_directory", "missing-package", "fixture-plugin", "", "entry.js", "", ast.ImportStmt, true},
		{"alias_suppresses_source_rewrite", "original", "", "local.js", "alias feature", "", ast.ImportStmt, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := root
			if tt.noResolveDir {
				dir = ""
			}
			text, suggestion, notes := ResolveFailureErrorTextSuggestionNotes(res, tt.path, tt.kind, tt.plugin, input, dir, config.PlatformBrowser, logger.PrettyPaths{Abs: "entry.js", Rel: "entry.js"}, tt.alias, logger.RelPath)
			var rendered strings.Builder
			for _, note := range notes {
				rendered.WriteString(note.Text)
			}
			if !strings.Contains(text, tt.path) || suggestion != tt.suggestion || !strings.Contains(rendered.String(), tt.hint) {
				t.Fatalf("resolution diagnostic = %q, suggestion %q, notes %q", text, suggestion, rendered.String())
			}
			if tt.alias != "" && (!strings.Contains(text, tt.alias) || len(notes) < 3 || notes[1].Text != "") {
				t.Fatalf("alias diagnostic lost remapping identity/paragraph boundary: %q / %+v", text, notes)
			}
		})
	}
}

func TestCatalogBundlerMediaAndPrintableText(t *testing.T) {
	for _, tt := range []struct {
		name, extension, contents, want string
	}{
		{"extension_over_content", ".CSS", "not css", "text/css;charset=utf-8"},
		{"sniff_html", ".unknown", "<!DOCTYPE HTML><p>content</p>", "text/html;charset=utf-8"},
		{"sniff_binary", "", "\x00\x01\x02", "application/octet-stream"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := guessMimeType(tt.extension, tt.contents); got != tt.want {
				t.Fatalf("media type = %q, want %q", got, tt.want)
			}
			if tt.extension == "" && tt.want != http.DetectContentType([]byte(tt.contents)) {
				t.Fatal("native sniffing result changed")
			}
		})
	}
	for _, tt := range []struct {
		text string
		want bool
	}{{"", true}, {"printable ~", true}, {"tab\t", false}, {"line\n", false}, {"\x7f", false}, {"café", false}} {
		if got := isASCIIOnly(tt.text); got != tt.want {
			t.Fatalf("printable-ASCII classification of %q = %v", tt.text, got)
		}
	}
}
