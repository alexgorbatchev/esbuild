package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/evanw/esbuild/internal/cli_helpers"
	"github.com/evanw/esbuild/pkg/api"
)

func catalogWriteFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func catalogRequireSuccess(t *testing.T, result api.BuildResult) {
	t.Helper()
	if len(result.Errors) != 0 {
		t.Fatalf("native build failed: %+v", result.Errors)
	}
	if len(result.OutputFiles) == 0 {
		t.Fatal("native build returned no outputs")
	}
}

func catalogOutput(t *testing.T, result api.BuildResult, suffix string) api.OutputFile {
	t.Helper()
	for _, file := range result.OutputFiles {
		if strings.HasSuffix(file.Path, suffix) {
			return file
		}
	}
	t.Fatalf("missing output %q in %+v", suffix, result.OutputFiles)
	return api.OutputFile{}
}

func catalogRequireText(t *testing.T, text string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Errorf("missing %q in %q", fragment, text)
		}
	}
}

func catalogRequireError(t *testing.T, messages []api.Message, fragment string) api.Message {
	t.Helper()
	for _, message := range messages {
		if strings.Contains(message.Text, fragment) {
			return message
		}
	}
	t.Fatalf("missing error %q in %+v", fragment, messages)
	return api.Message{}
}

type catalogMetafile struct {
	Inputs map[string]struct {
		Bytes   int `json:"bytes"`
		Imports []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"imports"`
	} `json:"inputs"`
	Outputs map[string]struct {
		Bytes      int    `json:"bytes"`
		EntryPoint string `json:"entryPoint"`
	} `json:"outputs"`
}

func TestCatalogAPINativeGraph(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"src/entry.ts":                     `import chosen from "chosen"; import data from "./data.json"; import "./style.css"; export const result: string = chosen + data.name + MODE; export const lazy = () => import("./lazy.ts");`,
		"src/lazy.ts":                      `import shared from "./shared.ts"; export default shared;`,
		"src/shared.ts":                    `export default 13;`,
		"src/data.json":                    `{"name":"json-native"}`,
		"src/style.css":                    `@import "./base.css"; .catalog { background: url("./asset.bin"); color: #ff0000; }`,
		"src/base.css":                     `.base { display: flex; }`,
		"src/asset.bin":                    "native-asset-bytes",
		"node_modules/chosen/package.json": `{"exports":{"catalog":"./catalog.js","default":"./default.js"}}`,
		"node_modules/chosen/catalog.js":   `export default "chosen-catalog";`,
		"node_modules/chosen/default.js":   `export default "wrong-condition";`,
	}
	for name, contents := range files {
		catalogWriteFile(t, dir, name, contents)
	}
	var mu sync.Mutex
	kinds := map[api.ResolveKind]bool{}
	observer := api.Plugin{Name: "native-import-observer", Setup: func(build api.PluginBuild) {
		build.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
			mu.Lock()
			kinds[args.Kind] = true
			mu.Unlock()
			return api.OnResolveResult{}, nil
		})
	}}
	options := api.BuildOptions{
		AbsWorkingDir: dir, EntryPointsAdvanced: []api.EntryPoint{{InputPath: "src/entry.ts", OutputPath: "report"}},
		Outdir: "out", Bundle: true, Splitting: true, Format: api.FormatESModule, Platform: api.PlatformNeutral,
		Target: api.ES2018, Engines: []api.Engine{{Name: api.EngineNode, Version: "18.0.0"}},
		Conditions: []string{"catalog"}, Loader: map[string]api.Loader{".bin": api.LoaderFile},
		Define:       map[string]string{"MODE": `"defined-target"`},
		OutExtension: map[string]string{".js": ".mjs"}, EntryNames: "[name]", ChunkNames: "chunks/[name]-[hash]", AssetNames: "assets/[name]-[hash]",
		PublicPath: "/catalog", Sourcemap: api.SourceMapExternal, SourceRoot: "/source", SourcesContent: api.SourcesContentExclude,
		Banner: map[string]string{"js": "// native banner", "css": "/* native css banner */"},
		Footer: map[string]string{"js": "// native footer"}, MinifySyntax: true, Metafile: true, Plugins: []api.Plugin{observer},
		LogLevel: api.LogLevelSilent,
	}
	result := api.Build(options)
	catalogRequireSuccess(t, result)
	entry := catalogOutput(t, result, "report.mjs")
	catalogRequireText(t, string(entry.Contents), "chosen-catalog", "json-native", "defined-target", "chunks/", "native banner", "native footer")
	if bytes.Contains(entry.Contents, []byte("wrong-condition")) {
		t.Fatal("unselected package condition was bundled")
	}
	catalogRequireText(t, string(catalogOutput(t, result, "report.css").Contents), "/catalog/assets/asset-", ".base", "native css banner")
	if !bytes.Equal(catalogOutput(t, result, ".bin").Contents, []byte(files["src/asset.bin"])) {
		t.Fatal("file loader changed asset bytes")
	}
	var sourceMap struct {
		SourcesRoot    string   `json:"sourceRoot"`
		Sources        []string `json:"sources"`
		SourcesContent []string `json:"sourcesContent"`
		Mappings       string   `json:"mappings"`
	}
	if err := json.Unmarshal(catalogOutput(t, result, "report.mjs.map").Contents, &sourceMap); err != nil {
		t.Fatal(err)
	}
	if sourceMap.SourcesRoot != "/source" || len(sourceMap.Sources) == 0 || sourceMap.Mappings == "" || sourceMap.SourcesContent != nil {
		t.Fatalf("generated map lost source identity or excluded content: %+v", sourceMap)
	}
	var metadata catalogMetafile
	if err := json.Unmarshal([]byte(result.Metafile), &metadata); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"src/entry.ts", "src/data.json", "src/base.css", "src/asset.bin", "node_modules/chosen/catalog.js"} {
		if input, ok := metadata.Inputs[name]; !ok || input.Bytes != len(files[name]) {
			t.Errorf("metafile input %q does not describe actual source bytes: %+v", name, input)
		}
	}
	for _, file := range result.OutputFiles {
		if file.Hash == "" {
			t.Errorf("output hash missing for %q", file.Path)
		}
		if strings.HasSuffix(file.Path, ".map") {
			continue // Source maps are intentionally excluded from the metafile.
		}
		name, err := filepath.Rel(dir, file.Path)
		if err != nil {
			t.Fatal(err)
		}
		if output, ok := metadata.Outputs[filepath.ToSlash(name)]; !ok || output.Bytes != len(file.Contents) {
			t.Errorf("metafile output %q does not describe actual generated bytes: %+v", name, output)
		}
	}
	for _, kind := range []api.ResolveKind{api.ResolveEntryPoint, api.ResolveJSImportStatement, api.ResolveJSDynamicImport, api.ResolveCSSImportRule, api.ResolveCSSURLToken} {
		if !kinds[kind] {
			t.Errorf("native import kind was not forwarded: %v", kind)
		}
	}
	for _, verbose := range []bool{false, true} {
		analysis := api.AnalyzeMetafile(result.Metafile, api.AnalyzeMetafileOptions{Verbose: verbose, Color: true})
		catalogRequireText(t, analysis, "report.mjs", "entry.ts")
	}
	if _, err := os.Stat(filepath.Join(dir, "out")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Write:false created native output directory: %v", err)
	}
	options.Write = true
	written := api.Build(options)
	catalogRequireSuccess(t, written)
	for _, file := range written.OutputFiles {
		contents, err := os.ReadFile(file.Path)
		if err != nil || !bytes.Equal(contents, file.Contents) {
			t.Fatalf("native write differs from returned output %q: %v", file.Path, err)
		}
	}
}

func TestCatalogAPITransform(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		options api.TransformOptions
		want    []string
		absent  []string
	}{
		{"typescript", `interface Tool { name: string }; const tool: Tool = { name: "native-ts" }; export default tool;`, api.TransformOptions{Loader: api.LoaderTS, Format: api.FormatCommonJS, Target: api.ES2018}, []string{"native-ts", "module.exports"}, []string{"interface Tool", "tool: Tool"}},
		{"jsx-classic", `export const view = <><strong>native-jsx</strong></>;`, api.TransformOptions{Loader: api.LoaderJSX, JSXFactory: "render", JSXFragment: "Fragment", JSXSideEffects: true}, []string{"render(Fragment", `render("strong"`}, []string{"<strong>"}},
		{"jsx-automatic", `export const view = <strong>native-auto</strong>;`, api.TransformOptions{Loader: api.LoaderTSX, JSX: api.JSXAutomatic, JSXImportSource: "preact", JSXDev: true, Sourcefile: "view.tsx"}, []string{"preact/jsx-dev-runtime", "jsxDEV", "view.tsx"}, []string{"<strong>"}},
		{"jsx-preserve", `const title: string = "preserved"; export const view = <b>{title}</b>;`, api.TransformOptions{Loader: api.LoaderTSX, JSX: api.JSXPreserve}, []string{"<b>{title}</b>"}, []string{"title: string"}},
		{"css", `.catalog { color: #ff0000; padding: 0px 0px 0px 0px; }`, api.TransformOptions{Loader: api.LoaderCSS, MinifySyntax: true, MinifyWhitespace: true}, []string{"color:red", "padding:0"}, []string{"#ff0000", "0px"}},
		{"json", `{"name":"json-transform","count":3}`, api.TransformOptions{Loader: api.LoaderJSON, Format: api.FormatESModule}, []string{"json-transform", "export"}, nil},
		{"text", "native text é", api.TransformOptions{Loader: api.LoaderText, Format: api.FormatESModule, Charset: api.CharsetASCII}, []string{`\xE9`, "export"}, []string{"é"}},
		{"lowering-and-drop", `debugger; console.log("removed-console"); TRACE: { trace(); } pure(); export const enabled = FLAG && (() => "enabled");`, api.TransformOptions{Loader: api.LoaderJS, Target: api.ES2015, Define: map[string]string{"FLAG": "true"}, Pure: []string{"pure"}, Drop: api.DropConsole | api.DropDebugger, DropLabels: []string{"TRACE"}, MinifySyntax: true, TreeShaking: api.TreeShakingTrue}, []string{"enabled"}, []string{"debugger", "console", "trace()", "pure()", "FLAG"}},
		{"iife-name", `export const name = "native-global";`, api.TransformOptions{Format: api.FormatIIFE, GlobalName: "catalog.tools", Banner: "/* transform banner */", Footer: "/* transform footer */", KeepNames: true}, []string{"catalog", "native-global", "transform banner", "transform footer"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.options.LogLevel = api.LogLevelSilent
			result := api.Transform(tt.input, tt.options)
			if len(result.Errors) != 0 || len(result.Code) == 0 {
				t.Fatalf("native transform failed: %+v", result)
			}
			code := string(result.Code)
			catalogRequireText(t, code, tt.want...)
			for _, fragment := range tt.absent {
				if strings.Contains(code, fragment) {
					t.Errorf("untransformed %q in %q", fragment, code)
				}
			}
		})
	}
}

func TestCatalogAPITransformArtifacts(t *testing.T) {
	input := `/*! native license */ export const object = { private_: 1, keep_: 2, "quoted_": 3 }; console.log(object.private_, object.keep_, object["quoted_"]);`
	cache := map[string]interface{}{"private_": "renamed", "keep_": false}
	options := api.TransformOptions{
		Loader: api.LoaderTS, Sourcefile: "catalog.ts", Sourcemap: api.SourceMapExternal,
		SourceRoot: "/original", SourcesContent: api.SourcesContentInclude,
		MangleProps: "_$", ReserveProps: "^keep_$", MangleQuoted: api.MangleQuotedTrue, MangleCache: cache,
		LegalComments: api.LegalCommentsExternal, MinifyIdentifiers: true, LogLevel: api.LogLevelSilent,
	}
	result := api.Transform(input, options)
	if len(result.Errors) != 0 {
		t.Fatal(result.Errors)
	}
	catalogRequireText(t, string(result.Code), "renamed", "keep_")
	if bytes.Contains(result.Code, []byte("private_")) || bytes.Contains(result.Code, []byte("quoted_")) || bytes.Contains(result.Code, []byte("native license")) {
		t.Fatalf("mangling/legal-comment extraction did not apply: %s", result.Code)
	}
	catalogRequireText(t, string(result.LegalComments), "native license")
	if result.MangleCache["private_"] != "renamed" || result.MangleCache["keep_"] != false || result.MangleCache["quoted_"] == nil {
		t.Fatalf("native mangle cache lost caller decisions or new mapping: %+v", result.MangleCache)
	}
	if _, ok := cache["quoted_"]; ok {
		t.Fatal("native transform mutated caller-owned mangle cache")
	}
	var sourceMap struct {
		Sources        []string `json:"sources"`
		SourcesContent []string `json:"sourcesContent"`
		Mappings       string   `json:"mappings"`
	}
	if err := json.Unmarshal(result.Map, &sourceMap); err != nil {
		t.Fatal(err)
	}
	if len(sourceMap.Sources) != 1 || sourceMap.Sources[0] != "catalog.ts" || len(sourceMap.SourcesContent) != 1 || sourceMap.SourcesContent[0] != input || sourceMap.Mappings == "" {
		t.Fatalf("native source map lost exact input: %+v", sourceMap)
	}
	options.Sourcemap = api.SourceMapInline
	options.LegalComments = api.LegalCommentsInline
	inline := api.Transform(input, options)
	if len(inline.Errors) != 0 || len(inline.Map) != 0 || len(inline.LegalComments) != 0 {
		t.Fatalf("inline artifacts were not emitted inline: %+v", inline)
	}
	catalogRequireText(t, string(inline.Code), "sourceMappingURL=data:application/json;base64,", "native license")
}

func TestCatalogAPILoaderParsing(t *testing.T) {
	const raw = "native-loader-bytes"
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"default", `export const loader = "native-loader";`, "native-loader"},
		{"js", `export const loader = "native-loader";`, "native-loader"},
		{"ts", `export const loader: string = "native-loader";`, "native-loader"},
		{"jsx", `export const loader = <b>native-loader</b>;`, "createElement"},
		{"tsx", `const value: string = "native-loader"; export const loader = <b>{value}</b>;`, "createElement"},
		{"json", `{"name":"native-loader"}`, "native-loader"},
		{"text", raw, raw},
		{"base64", raw, base64.StdEncoding.EncodeToString([]byte(raw))},
		{"binary", raw, "Uint8Array"},
		{"dataurl", raw, "data:"},
		{"empty", "invalid input that the empty loader ignores", ""},
		{"css", `.catalog { color: red; }`, "color: red"},
		{"global-css", `.catalog { color: red; }`, ".catalog"},
		{"local-css", `.catalog { color: red; }`, "color: red"},
		{"file", raw, ""},
		{"copy", raw, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader, err := cli_helpers.ParseLoader(tt.name)
			if err != nil {
				t.Fatalf("loader parsing rejected native transform input: %+v", err)
			}
			if tt.name == "file" || tt.name == "copy" {
				dir := t.TempDir()
				catalogWriteFile(t, dir, "input.bin", tt.input)
				entry := catalogWriteFile(t, dir, "entry.js", `import asset from "./input.bin"; export default asset;`)
				result := api.Build(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outdir: "out", Bundle: true, Format: api.FormatESModule, Loader: map[string]api.Loader{".bin": loader}, LogLevel: api.LogLevelSilent})
				catalogRequireSuccess(t, result)
				if !bytes.Equal(catalogOutput(t, result, ".bin").Contents, []byte(tt.input)) {
					t.Fatal("parsed asset loader changed native input bytes")
				}
				catalogRequireText(t, string(catalogOutput(t, result, "entry.js").Contents), "input-", ".bin")
				return
			}
			result := api.Transform(tt.input, api.TransformOptions{Loader: loader, Sourcefile: "catalog-input", LogLevel: api.LogLevelSilent})
			if len(result.Errors) != 0 {
				t.Fatal(result.Errors)
			}
			if tt.name == "empty" {
				if len(result.Code) != 0 {
					t.Fatalf("empty loader compiled ignored input: %q", result.Code)
				}
			} else {
				catalogRequireText(t, string(result.Code), tt.want)
			}
		})
	}
	_, err := cli_helpers.ParseLoader("unsupported-catalog")
	if err == nil || !strings.Contains(err.Text, "unsupported-catalog") || !strings.Contains(err.Note, "Valid values are") {
		t.Fatalf("unknown loader did not retain input and remediation: %+v", err)
	}
}

func TestCatalogAPIOptionDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		edit func(*api.BuildOptions)
		want string
	}{
		{"two-output-locations", func(o *api.BuildOptions) { o.Outfile = "out.js"; o.Outdir = "out" }, "Cannot use both"},
		{"multiple-entry-points", func(o *api.BuildOptions) { o.Stdin = nil; o.EntryPoints = []string{"one.js", "two.js"} }, "multiple input files"},
		{"splitting-output", func(o *api.BuildOptions) { o.Splitting = true }, "code splitting is enabled"},
		{"splitting-format", func(o *api.BuildOptions) { o.Outdir = "out"; o.Splitting = true; o.Format = api.FormatCommonJS }, "Splitting currently only works"},
		{"external-map-without-output", func(o *api.BuildOptions) { o.Sourcemap = api.SourceMapExternal }, "external source map without an output path"},
		{"external-comments-without-output", func(o *api.BuildOptions) { o.LegalComments = api.LegalCommentsLinked }, "legal comments without an output path"},
		{"file-loader-without-output", func(o *api.BuildOptions) { o.Loader = map[string]api.Loader{".bin": api.LoaderFile} }, `"file" loader without an output path`},
		{"copy-loader-without-output", func(o *api.BuildOptions) { o.Loader = map[string]api.Loader{".bin": api.LoaderCopy} }, `"copy" loader without an output path`},
		{"external-without-bundle", func(o *api.BuildOptions) { o.External = []string{"dependency"} }, `"external" without "bundle"`},
		{"alias-without-bundle", func(o *api.BuildOptions) { o.Alias = map[string]string{"dependency": "other"} }, `"alias" without "bundle"`},
		{"alias-name", func(o *api.BuildOptions) { o.Bundle = true; o.Alias = map[string]string{"./bad": "other"} }, "Invalid alias name"},
		{"alias-substitution", func(o *api.BuildOptions) { o.Bundle = true; o.Alias = map[string]string{"valid": ""} }, "Invalid alias substitution"},
		{"external-wildcards", func(o *api.BuildOptions) { o.Bundle = true; o.External = []string{"a*b*c"} }, "more than one"},
		{"resolve-extension", func(o *api.BuildOptions) { o.ResolveExtensions = []string{"ts"} }, "Invalid file extension"},
		{"loader-extension", func(o *api.BuildOptions) { o.Loader = map[string]api.Loader{"bad": api.LoaderJS} }, "Invalid file extension"},
		{"output-extension-key", func(o *api.BuildOptions) { o.OutExtension = map[string]string{".bad": ".js"} }, "Invalid output extension"},
		{"output-extension-value", func(o *api.BuildOptions) { o.OutExtension = map[string]string{".js": "js"} }, "Invalid output extension"},
		{"banner-type", func(o *api.BuildOptions) { o.Banner = map[string]string{"html": "banner"} }, "Invalid banner file type"},
		{"footer-type", func(o *api.BuildOptions) { o.Footer = map[string]string{"html": "footer"} }, "Invalid footer file type"},
		{"mangle-regexp", func(o *api.BuildOptions) { o.MangleProps = "[" }, `"mangle props" setting is not a valid Go regular expression`},
		{"jsx-factory", func(o *api.BuildOptions) { o.JSXFactory = "a+b" }, "Invalid JSX factory"},
		{"jsx-fragment", func(o *api.BuildOptions) { o.JSXFragment = "a+b" }, "Invalid JSX fragment"},
		{"global-name", func(o *api.BuildOptions) { o.GlobalName = "a+b" }, `Expected "." but found "+"`},
		{"define-name", func(o *api.BuildOptions) { o.Define = map[string]string{"a+b": "1"} }, `Expected "." but found "+"`},
		{"define-value", func(o *api.BuildOptions) { o.Define = map[string]string{"valid": "a+b"} }, "Invalid define value"},
		{"pure-name", func(o *api.BuildOptions) { o.Pure = []string{"a+b"} }, `Expected "." but found "+"`},
		{"engine-version", func(o *api.BuildOptions) { o.Engines = []api.Engine{{Name: api.EngineNode, Version: "invalid"}} }, "Invalid version"},
		{"unknown-feature", func(o *api.BuildOptions) { o.Supported = map[string]bool{"catalog-unknown": true} }, "not a valid feature name"},
		{"keep-names-target", func(o *api.BuildOptions) { o.KeepNames = true; o.Target = api.ES5 }, `"keep names" setting cannot be used`},
		{"tsconfig-conflict", func(o *api.BuildOptions) { o.Tsconfig = "tsconfig.json"; o.TsconfigRaw = "{}" }, `"tsconfig" as both`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := api.BuildOptions{AbsWorkingDir: t.TempDir(), Stdin: &api.StdinOptions{Contents: "export const value = 1", Loader: api.LoaderJS}, LogLevel: api.LogLevelSilent}
			tt.edit(&options)
			result := api.Build(options)
			catalogRequireError(t, result.Errors, tt.want)
			if len(result.OutputFiles) != 0 || result.MangleCache != nil {
				t.Fatalf("invalid build exposed successful outputs: %+v", result)
			}
		})
	}
}

func TestCatalogAPIInvalidMangleCache(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  string
	}{
		{"protocol-identifier", "__proto__", "Invalid identifier name"},
		{"unsupported-type", true, "either a string or false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := api.Build(api.BuildOptions{Stdin: &api.StdinOptions{Contents: "export const value = 1", Loader: api.LoaderJS}, MangleCache: map[string]interface{}{"property": tt.value}, LogLevel: api.LogLevelSilent})
			catalogRequireError(t, result.Errors, tt.want)
			if result.MangleCache != nil {
				t.Fatal("failed build returned a successful mangle cache")
			}
			// Native cache validation happens after scanning. Outputs alone do not
			// establish success; callers must reject the nonempty Errors result.
			if len(result.OutputFiles) == 0 {
				t.Fatal("native late-validation output behavior changed")
			}
		})
	}
}

func TestCatalogAPISourceDiagnostics(t *testing.T) {
	input := "// diagnostic source\nexport const good = 1;\nexport const broken = ;\n"
	result := api.Transform(input, api.TransformOptions{Loader: api.LoaderTS, Sourcefile: "broken.tool.ts", LogLevel: api.LogLevelSilent})
	message := catalogRequireError(t, result.Errors, "Unexpected")
	if message.Location == nil || message.Location.File != "broken.tool.ts" || message.Location.Line != 3 || message.Location.Column != len("export const broken = ") || message.Location.LineText != "export const broken = ;" {
		t.Fatalf("source diagnostic lost original byte location: %+v", message)
	}
	formatted := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage, TerminalWidth: 80, Color: true})
	if len(formatted) != len(result.Errors) {
		t.Fatal("diagnostic formatting lost a native error")
	}
	catalogRequireText(t, strings.Join(formatted, ""), "broken.tool.ts:3:", "export const broken", "ERROR")
	warning := api.Transform("console.log(process.env.NODE_ENV)", api.TransformOptions{Define: map[string]string{"process.env.NODE_ENV": "production"}, LogLevel: api.LogLevelSilent})
	if len(warning.Errors) != 0 || len(warning.Warnings) == 0 || warning.Warnings[0].Location == nil || warning.Warnings[0].Location.Suggestion == "" {
		t.Fatalf("suspicious define warning lost its correction: %+v", warning)
	}
	options := api.BuildOptions{Stdin: &api.StdinOptions{Contents: input, Sourcefile: "broken.tool.ts", Loader: api.LoaderTS}, LogLevel: api.LogLevelSilent, Outfile: "out.js", Outdir: "out"}
	ctx, err := api.Context(options)
	if ctx != nil || err == nil || !strings.Contains(err.Error(), "Cannot use both") || len(err.Errors) == 0 {
		t.Fatalf("invalid context did not return native option diagnostics: context=%v error=%v", ctx, err)
	}
}

func TestCatalogAPIContextRebuild(t *testing.T) {
	dir := t.TempDir()
	entry := catalogWriteFile(t, dir, "entry.ts", `export const value: string = "first-native";`)
	disposed := make(chan struct{})
	ctx, err := api.Context(api.BuildOptions{
		AbsWorkingDir: dir, EntryPoints: []string{entry}, Outfile: "out.js", Format: api.FormatESModule, LogLevel: api.LogLevelSilent,
		Plugins: []api.Plugin{{Name: "native-lifetime", Setup: func(build api.PluginBuild) { build.OnDispose(func() { close(disposed) }) }}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Dispose()
	first := ctx.Rebuild()
	catalogRequireSuccess(t, first)
	catalogRequireText(t, string(first.OutputFiles[0].Contents), "first-native")
	catalogWriteFile(t, dir, "entry.ts", `export const value: string = "second-native-longer";`)
	second := ctx.Rebuild()
	catalogRequireSuccess(t, second)
	catalogRequireText(t, string(second.OutputFiles[0].Contents), "second-native-longer")
	if first.OutputFiles[0].Hash == second.OutputFiles[0].Hash {
		t.Fatal("native rebuild retained the previous output identity")
	}
	catalogWriteFile(t, dir, "entry.ts", `export const value = ;`)
	broken := ctx.Rebuild()
	catalogRequireError(t, broken.Errors, "Unexpected")
	if len(broken.OutputFiles) != 0 {
		t.Fatal("failed native rebuild exposed old output")
	}
	catalogWriteFile(t, dir, "entry.ts", `export const value = "restored-native";`)
	restored := ctx.Rebuild()
	catalogRequireSuccess(t, restored)
	catalogRequireText(t, string(restored.OutputFiles[0].Contents), "restored-native")
	ctx.Cancel() // Idle cancellation must leave the next real build usable.
	catalogRequireSuccess(t, ctx.Rebuild())
	ctx.Dispose()
	const callbackDeadline = 5 * time.Second
	select {
	case <-disposed:
	case <-time.After(callbackDeadline):
		t.Fatal("native dispose callback did not complete")
	}
	ctx.Dispose()
	ctx.Cancel()
	if result := ctx.Rebuild(); len(result.OutputFiles) != 0 || len(result.Errors) != 0 {
		t.Fatalf("disposed context started another build: %+v", result)
	}
	if err := ctx.Watch(api.WatchOptions{}); err == nil || !strings.Contains(err.Error(), "disposed") {
		t.Fatalf("disposed context accepted watch: %v", err)
	}
}

func TestCatalogAPIContextCancel(t *testing.T) {
	// Wait establishes that Cancel has reached the real build's wait group;
	// no timer or scheduler guess decides when the plugin may finish loading.
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		entered := make(chan struct{})
		ended := make(chan api.BuildResult, 2)
		var once sync.Once
		plugin := api.Plugin{Name: "native-cancel-barrier", Setup: func(build api.PluginBuild) {
			build.OnLoad(api.OnLoadOptions{Filter: ".*"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				once.Do(func() { close(entered); <-release })
				contents := `export const value = "native-after-cancel";`
				return api.OnLoadResult{Contents: &contents, Loader: api.LoaderTS}, nil
			})
			build.OnEnd(func(result *api.BuildResult) (api.OnEndResult, error) {
				ended <- *result
				return api.OnEndResult{}, nil
			})
		}}
		dir := t.TempDir()
		entry := catalogWriteFile(t, dir, "entry.ts", `export const original = 1;`)
		ctx, err := api.Context(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outfile: "out.js", Plugins: []api.Plugin{plugin}, LogLevel: api.LogLevelSilent})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			ctx.Dispose()
			// Drain native recent-build expiry goroutines even after an assertion fails.
			const recentBuildExpiry = time.Second
			time.Sleep(recentBuildExpiry)
			synctest.Wait()
		}()
		built := make(chan api.BuildResult, 1)
		go func() { built <- ctx.Rebuild() }()
		<-entered
		shared := make(chan api.BuildResult, 1)
		go func() { shared <- ctx.Rebuild() }()
		cancelDone := make(chan struct{})
		go func() { ctx.Cancel(); close(cancelDone) }()
		synctest.Wait()
		select {
		case <-cancelDone:
			t.Error("Cancel returned before the active plugin and build finished")
		default:
		}
		close(release)
		<-cancelDone
		canceled := <-built
		sharedResult := <-shared
		catalogRequireError(t, canceled.Errors, "build was canceled")
		catalogRequireError(t, sharedResult.Errors, "build was canceled")
		catalogRequireError(t, (<-ended).Errors, "build was canceled")
		if len(canceled.OutputFiles) != 0 {
			t.Fatal("canceled native build returned successful outputs")
		}
		restored := ctx.Rebuild()
		catalogRequireSuccess(t, restored)
		catalogRequireText(t, string(restored.OutputFiles[0].Contents), "native-after-cancel")
		catalogRequireSuccess(t, <-ended)
	})
}

func TestCatalogAPIPluginResolution(t *testing.T) {
	dir := t.TempDir()
	entry := catalogWriteFile(t, dir, "entry.ts", `import value from "catalog-virtual"; export default value;`)
	shared := catalogWriteFile(t, dir, "shared.ts", `export default "native-shared";`)
	var early, missingKind, resolved, missing api.ResolveResult
	var loaded api.OnLoadArgs
	detail := errors.New("preserved warning detail")
	plugin := api.Plugin{Name: "native-resolution", Setup: func(build api.PluginBuild) {
		build.InitialOptions.Define = map[string]string{"PLUGIN_SETUP": `"setup-native"`}
		early = build.Resolve("./shared.ts", api.ResolveOptions{Kind: api.ResolveJSImportStatement, ResolveDir: dir})
		build.OnStart(func() (api.OnStartResult, error) {
			missingKind = build.Resolve("./shared.ts", api.ResolveOptions{ResolveDir: dir})
			resolved = build.Resolve("./shared.ts", api.ResolveOptions{Kind: api.ResolveJSRequireCall, ResolveDir: dir, Namespace: "file"})
			missing = build.Resolve("./absent.ts", api.ResolveOptions{Kind: api.ResolveJSDynamicImport, ResolveDir: dir, PluginName: "requested-resolver"})
			return api.OnStartResult{}, nil
		})
		build.OnResolve(api.OnResolveOptions{Filter: "^catalog-virtual$"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
			if args.Kind != api.ResolveJSImportStatement || args.Importer != entry || args.ResolveDir != dir {
				return api.OnResolveResult{}, errors.New("native resolve lost importer identity")
			}
			return api.OnResolveResult{Path: "catalog-virtual", Namespace: "catalog", Suffix: "?selected", PluginData: detail, WatchFiles: []string{shared}, WatchDirs: []string{dir}}, nil
		})
		build.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "catalog"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
			loaded = args
			contents := `import shared from "./shared.ts"; export default shared + PLUGIN_SETUP;`
			return api.OnLoadResult{Contents: &contents, Loader: api.LoaderTS, ResolveDir: dir, WatchFiles: []string{shared}, WatchDirs: []string{dir}, Warnings: []api.Message{{Text: "plugin-native-warning", Detail: detail, Location: &api.Location{File: "virtual.ts", Line: 1, LineText: contents}, Notes: []api.Note{{Text: "native note"}}}}}, nil
		})
		build.OnEnd(func(result *api.BuildResult) (api.OnEndResult, error) {
			if len(result.OutputFiles) == 0 || result.Metafile == "" {
				return api.OnEndResult{}, errors.New("native onEnd lost build artifacts")
			}
			return api.OnEndResult{Warnings: []api.Message{{Text: "onEnd-native-warning"}}}, nil
		})
	}}
	result := api.Build(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Outfile: "out.js", Bundle: true, Metafile: true, Plugins: []api.Plugin{plugin}, LogLevel: api.LogLevelSilent})
	catalogRequireSuccess(t, result)
	catalogRequireText(t, string(result.OutputFiles[0].Contents), "native-shared", "setup-native")
	catalogRequireError(t, early.Errors, "before plugin setup has completed")
	catalogRequireError(t, missingKind.Errors, `specify "kind"`)
	catalogRequireError(t, missing.Errors, "Could not resolve")
	if len(resolved.Errors) != 0 || resolved.Path != shared || resolved.Namespace != "file" || resolved.External {
		t.Fatalf("plugin Resolve failed native path resolution: %+v", resolved)
	}
	if loaded.Path != "catalog-virtual" || loaded.Namespace != "catalog" || loaded.Suffix != "?selected" || loaded.PluginData != detail {
		t.Fatalf("plugin load lost resolution metadata: %+v", loaded)
	}
	message := catalogRequireError(t, result.Warnings, "plugin-native-warning")
	if message.Detail != detail || message.PluginName != "native-resolution" || message.Location == nil || message.Location.File != "virtual.ts" || len(message.Notes) != 2 || message.Notes[0].Text != "native note" || !strings.Contains(message.Notes[1].Text, "triggered by this import") || message.Notes[1].Location == nil {
		t.Fatalf("plugin warning lost native metadata: %+v", message)
	}
	catalogRequireError(t, result.Warnings, "onEnd-native-warning")
}

func TestCatalogAPIPluginFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(api.PluginBuild, error)
		want  string
	}{
		{"start", func(b api.PluginBuild, err error) {
			b.OnStart(func() (api.OnStartResult, error) { return api.OnStartResult{}, err })
		}, "native-plugin-failure"},
		{"resolve", func(b api.PluginBuild, err error) {
			b.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(api.OnResolveArgs) (api.OnResolveResult, error) { return api.OnResolveResult{}, err })
		}, "native-plugin-failure"},
		{"load", func(b api.PluginBuild, err error) {
			b.OnLoad(api.OnLoadOptions{Filter: ".*"}, func(api.OnLoadArgs) (api.OnLoadResult, error) { return api.OnLoadResult{}, err })
		}, "native-plugin-failure"},
		{"end", func(b api.PluginBuild, err error) {
			b.OnEnd(func(*api.BuildResult) (api.OnEndResult, error) { return api.OnEndResult{}, err })
		}, "native-plugin-failure"},
		{"resolve-filter", func(b api.PluginBuild, _ error) {
			b.OnResolve(api.OnResolveOptions{Filter: "["}, func(api.OnResolveArgs) (api.OnResolveResult, error) { return api.OnResolveResult{}, nil })
		}, "not a valid Go regular expression"},
		{"load-filter", func(b api.PluginBuild, _ error) {
			b.OnLoad(api.OnLoadOptions{Filter: "["}, func(api.OnLoadArgs) (api.OnLoadResult, error) { return api.OnLoadResult{}, nil })
		}, "not a valid Go regular expression"},
		{"resolve-suffix", func(b api.PluginBuild, _ error) {
			b.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: args.Path, Namespace: "catalog", Suffix: "invalid"}, nil
			})
		}, "Invalid path suffix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			entry := catalogWriteFile(t, dir, "entry.js", `export const value = 1;`)
			detail := errors.New("native-plugin-failure")
			plugin := api.Plugin{Name: "native-failure", Setup: func(build api.PluginBuild) { tt.setup(build, detail) }}
			result := api.Build(api.BuildOptions{AbsWorkingDir: dir, EntryPoints: []string{entry}, Plugins: []api.Plugin{plugin}, LogLevel: api.LogLevelSilent})
			message := catalogRequireError(t, result.Errors, tt.want)
			if tt.name == "resolve-filter" || tt.name == "load-filter" {
				catalogRequireText(t, message.Text, "[native-failure]")
			} else if message.PluginName != "native-failure" {
				t.Fatalf("native error lost plugin attribution: %+v", message)
			}
			if tt.name == "end" {
				if message.Detail != nil {
					t.Fatalf("native thrown OnEnd error gained unprovided Detail: %+v", message)
				}
			} else if tt.want == detail.Error() && message.Detail != detail {
				t.Fatalf("native callback error identity was lost: %+v", message)
			}
		})
	}
}
