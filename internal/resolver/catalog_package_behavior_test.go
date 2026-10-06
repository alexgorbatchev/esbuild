package resolver

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanw/esbuild/internal/ast"
	"github.com/evanw/esbuild/internal/config"
	"github.com/evanw/esbuild/internal/logger"
)

func TestCatalogPackageConditionFallback(t *testing.T) {
	for _, tt := range []struct {
		name, exports, want string
		kind                ast.ImportKind
	}{
		{"nested_import", `{"node":{"import":"./import.js","require":"./require.js"},"default":"./fallback.js"}`, "import.js", ast.ImportStmt},
		{"nested_require", `{"node":{"import":"./import.js","require":"./require.js"},"default":"./fallback.js"}`, "require.js", ast.ImportRequire},
		{"nested_unmatched_parent_fallback", `{"node":{"browser":"./browser.js"},"default":"./fallback.js"}`, "fallback.js", ast.ImportStmt},
		{"nested_empty_parent_fallback", `{"node":{},"default":"./fallback.js"}`, "fallback.js", ast.ImportStmt},
		{"undefined_array_entries_fallback", `[{}, {"browser":"./browser.js"}, "./fallback.js"]`, "fallback.js", ast.ImportStmt},
		{"invalid_and_undefined_array_fallback", `["../outside.js",{},"./fallback.js"]`, "fallback.js", ast.ImportStmt},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, log, root := catalogNativeResolver(t, map[string]string{
				"node_modules/conditions/package.json": `{"exports":` + tt.exports + `}`,
				"node_modules/conditions/import.js":    "export default 'import'",
				"node_modules/conditions/require.js":   "module.exports = 'require'",
				"node_modules/conditions/browser.js":   "export default 'browser'",
				"node_modules/conditions/fallback.js":  "export default 'fallback'",
			}, config.Options{Platform: config.PlatformNode})
			result, _ := res.Resolve(root, "conditions", tt.kind)
			catalogResolvedFile(t, result, root, "node_modules/conditions/"+tt.want)
			if log.HasErrors() {
				t.Fatalf("condition fallback emitted errors: %+v", log.Done())
			}
		})
	}
}

func TestCatalogPackageMapRefusalDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name, exports, request, reason, token string
	}{
		{"nested_unmatched_condition", `{"node":{"require":"./good.js"}}`, "target", `package definition ("require")`, "{"},
		{"empty_target_array", `[]`, "target", "not exported", "["},
		{"invalid_then_null_target_array", `["../outside.js",null]`, "target", "explicitly disabled", "null"},
		{"only_subpath_encapsulates_root", `{"./feature":"./good.js"}`, "target", "not exported", "{"},
		{"pattern_parent_segment", `{"./features/*":"./lib/*.js"}`, "target/features/nested/../item", "invalid segment", `"./lib/*.js"`},
		{"pattern_node_modules_segment", `{"./features/*":"./lib/*.js"}`, "target/features/nested/node_modules/item", "invalid segment", `"./lib/*.js"`},
		{"mixed_conditions_and_subpaths", `{".":"./good.js","node":"./good.js"}`, "target", "invalid value", "{"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manifest := `{"main":"./good.js","exports":` + tt.exports + `}`
			res, log, root := catalogNativeResolver(t, map[string]string{
				"node_modules/target/package.json":                    manifest,
				"node_modules/target/good.js":                         "export default 'legacy main'",
				"node_modules/target/lib/item.js":                     "export default 'normalized path'",
				"node_modules/target/lib/nested/node_modules/item.js": "export default 'nested dependency'",
			}, config.Options{Platform: config.PlatformNode})
			result, meta := res.Resolve(root, tt.request, ast.ImportStmt)
			if result != nil {
				t.Fatalf("rejected package map reached a real module: %+v", result)
			}
			manifestPath := filepath.Join(root, "node_modules", "target", "package.json")
			catalogPackageNote(t, meta.notes, manifestPath, manifest, tt.reason, tt.token)
			if tt.name == "nested_unmatched_condition" {
				if !strings.Contains(meta.suggestionMessage, `"require()"`) {
					t.Fatalf("nested condition rejection lost its usable require suggestion: %q", meta.suggestionMessage)
				}
			}
			if tt.name == "mixed_conditions_and_subpaths" {
				var found bool
				for _, msg := range log.Done() {
					if msg.Kind == logger.Warning && strings.Contains(msg.Data.Text, `keys that both start with "."`) {
						catalogPackageNote(t, []logger.MsgData{msg.Data}, manifestPath, manifest, "keys", `"node"`)
						catalogPackageNote(t, msg.Notes, manifestPath, manifest, "incompatible", `"."`)
						found = true
					}
				}
				if !found {
					t.Fatal("mixed package map lost its source-bound warning")
				}
			}
		})
	}
}

func catalogPackageNote(t *testing.T, notes []logger.MsgData, path, manifest, reason, token string) {
	t.Helper()
	for _, note := range notes {
		loc := note.Location
		if strings.Contains(note.Text, reason) && loc != nil && loc.File.Abs == path && loc.LineText == manifest &&
			loc.Column >= 0 && loc.Column+loc.Length <= len(manifest) && manifest[loc.Column:loc.Column+loc.Length] == token {
			return
		}
	}
	t.Fatalf("package rejection lost reason %q at original token %q: %+v", reason, token, notes)
}

func TestCatalogPackagePrivatePatternImports(t *testing.T) {
	files := map[string]string{
		"package.json":                         `{"imports":{"#dependency/*":"dependency/features/*.js","#local/*":{"node":{"browser":"./browser/*.js"},"default":"./local/*.js"}}}`,
		"node_modules/dependency/package.json": `{"exports":{"./features/*.js":"./lib/*.js"}}`,
		"node_modules/dependency/lib/item.js":  "export default 'external pattern'",
		"local/item.js":                        "export default 'local fallback'",
		"browser/item.js":                      "export default 'browser'",
	}
	for _, tt := range []struct{ request, want string }{
		{"#dependency/item", "node_modules/dependency/lib/item.js"},
		{"#local/item", "local/item.js"},
	} {
		t.Run(tt.request, func(t *testing.T) {
			res, log, root := catalogNativeResolver(t, files, config.Options{Platform: config.PlatformNode})
			result, _ := res.Resolve(root, tt.request, ast.ImportStmt)
			catalogResolvedFile(t, result, root, tt.want)
			if log.HasErrors() {
				t.Fatalf("private pattern import emitted errors: %+v", log.Done())
			}
		})
	}
}
