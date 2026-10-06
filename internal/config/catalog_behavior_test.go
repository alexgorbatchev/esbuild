package config

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/evanw/esbuild/internal/compat"
	"github.com/evanw/esbuild/internal/logger"
)

func TestCatalogExtendedTypeScriptConfiguration(t *testing.T) {
	base := TSConfig{ExperimentalDecorators: True, ImportsNotUsedAsValues: TSImportsNotUsedAsValues_Error,
		PreserveValueImports: True, Target: TSTargetAtOrAboveES2022, UseDefineForClassFields: True, VerbatimModuleSyntax: True}
	var derived TSConfig
	derived.ApplyExtendedConfig(base)
	derived.ApplyExtendedConfig(TSConfig{})
	if derived != base {
		t.Fatalf("unspecified layer erased inherited fields: %+v", derived)
	}
	derived.ApplyExtendedConfig(TSConfig{ExperimentalDecorators: False, PreserveValueImports: False,
		UseDefineForClassFields: False, VerbatimModuleSyntax: False, ImportsNotUsedAsValues: TSImportsNotUsedAsValues_Remove, Target: TSTargetBelowES2022})
	if derived.UnusedImportFlags() != 0 || derived.UseDefineForClassFields != False || derived.Target != TSTargetBelowES2022 || derived.ExperimentalDecorators != False {
		t.Fatalf("explicit false/remove layer was ignored: %+v", derived)
	}
	for _, tt := range []struct {
		name string
		cfg  TSConfig
		want TSUnusedImportFlags
	}{
		{"values", TSConfig{PreserveValueImports: True}, TSUnusedImport_KeepValues},
		{"statements", TSConfig{ImportsNotUsedAsValues: TSImportsNotUsedAsValues_Preserve}, TSUnusedImport_KeepStmt},
		{"error", TSConfig{ImportsNotUsedAsValues: TSImportsNotUsedAsValues_Error}, TSUnusedImport_KeepStmt},
		{"verbatim", TSConfig{VerbatimModuleSyntax: True}, TSUnusedImport_KeepStmt | TSUnusedImport_KeepValues},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.UnusedImportFlags(); got != tt.want {
				t.Fatalf("unused import behavior = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCatalogJSXConfigurationLayers(t *testing.T) {
	importSource := "custom-runtime"
	var inherited TSConfigJSX
	inherited.ApplyExtendedConfig(TSConfigJSX{JSXFactory: []string{"h"}, JSXFragmentFactory: []string{"Fragment"}, JSXImportSource: &importSource, JSX: TSJSXReactJSXDev})
	inherited.ApplyExtendedConfig(TSConfigJSX{})
	options := JSXOptions{}
	inherited.ApplyTo(&options)
	if !options.AutomaticRuntime || !options.Development || options.ImportSource != importSource || strings.Join(options.Factory.Parts, ".") != "h" || strings.Join(options.Fragment.Parts, ".") != "Fragment" {
		t.Fatalf("inherited JSX options = %+v", options)
	}
	for _, tt := range []struct {
		name                   string
		mode                   TSJSX
		automatic, development bool
	}{
		{"classic", TSJSXReact, false, false},
		{"automatic_cli_dev", TSJSXReactJSX, true, true},
		{"automatic_dev", TSJSXReactJSXDev, true, true},
		{"preserve_cli", TSJSXPreserve, false, true},
		{"react_native_cli", TSJSXReactNative, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := JSXOptions{Development: true, Preserve: true}
			cfg := TSConfigJSX{JSX: tt.mode}
			cfg.ApplyTo(&options)
			if options.AutomaticRuntime != tt.automatic || options.Development != tt.development || !options.Preserve {
				t.Fatalf("JSX mode changed explicit CLI options: %+v", options)
			}
		})
	}
}

func TestCatalogOutputPathSubstitution(t *testing.T) {
	dir, name, hash, ext := "pages", "index", "ABC", "js"
	template := []PathTemplate{{Data: "build/", Placeholder: DirPlaceholder}, {Data: "/", Placeholder: NamePlaceholder}, {Data: "-", Placeholder: HashPlaceholder}, {Data: ".", Placeholder: ExtPlaceholder}}
	original := append([]PathTemplate(nil), template...)
	if got := TemplateToString(template); got != "build/[dir]/[name]-[hash].[ext]" {
		t.Fatalf("template = %q", got)
	}
	partial := SubstituteTemplate(template, PathPlaceholders{Dir: &dir, Name: &name, Ext: &ext})
	if got := TemplateToString(partial); got != "build/pages/index-[hash].js" {
		t.Fatalf("partial substitution = %q", got)
	}
	final := SubstituteTemplate(partial, PathPlaceholders{Hash: &hash})
	if got := TemplateToString(final); got != "build/pages/index-ABC.js" || len(final) != 1 || !reflect.DeepEqual(template, original) {
		t.Fatalf("final substitution/caller ownership = %q / %+v", got, template)
	}
	if !HasPlaceholder(partial, HashPlaceholder) || HasPlaceholder(final, HashPlaceholder) {
		t.Fatal("placeholder presence did not follow substitution")
	}
	minimal := []PathTemplate{{Data: "fixed.js"}}
	if got := SubstituteTemplate(minimal, PathPlaceholders{}); &got[0] != &minimal[0] {
		t.Fatal("minimal unsubstituted template was copied")
	}
	merged := SubstituteTemplate([]PathTemplate{{Data: "a"}, {Data: "b"}}, PathPlaceholders{})
	if len(merged) != 1 || TemplateToString(merged) != "ab" || TemplateToString(nil) != "" {
		t.Fatalf("literal coalescing = %+v", merged)
	}
}

func TestCatalogPluginFilterBehavior(t *testing.T) {
	for _, filter := range []string{"", "[", "(?=x)"} {
		if result, err := CompileFilterForPlugin("catalog", "onLoad", filter); err == nil || result != nil || !strings.Contains(err.Error(), "catalog") || !strings.Contains(err.Error(), "onLoad") {
			t.Fatalf("invalid filter %q diagnostic = %v / %v", filter, result, err)
		}
	}
	filter, err := CompileFilterForPlugin("catalog", "onLoad", `\.tool\.ts$`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path      logger.Path
		namespace string
		want      bool
	}{
		{logger.Path{Text: "/tools/bat.tool.ts", Namespace: "file"}, "file", true},
		{logger.Path{Text: "/tools/bat.tool.ts", Namespace: "virtual"}, "file", false},
		{logger.Path{Text: "/tools/bat.tool.ts", Namespace: "virtual"}, "", true},
		{logger.Path{Text: "/tools/bat.tool.ts.js", Namespace: "file"}, "file", false},
	} {
		if got := PluginAppliesToPath(tt.path, filter, tt.namespace); got != tt.want {
			t.Fatalf("filter(%+v, %q) = %v", tt.path, tt.namespace, got)
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			cached, err := CompileFilterForPlugin("catalog", "onLoad", `\.tool\.ts$`)
			if err != nil || !cached.MatchString("bat.tool.ts") {
				t.Errorf("concurrent cached filter failed: %v", err)
			}
		})
	}
	wg.Wait()
}

func TestCatalogTargetAndMetafileFormatting(t *testing.T) {
	for _, tt := range []struct {
		env  string
		mask compat.JSFeature
		want string
	}{
		{"", 0, "the configured target environment"},
		{"es2020", 0, "the configured target environment (es2020)"},
		{"es2020", 1, "the configured target environment (es2020 + 1 override)"},
		{"es2020", 5, "the configured target environment (es2020 + 2 overrides)"},
		{"", 5, "the configured target environment"},
	} {
		if got := PrettyPrintTargetEnvironment(tt.env, tt.mask); got != tt.want {
			t.Fatalf("target description = %q, want %q", got, tt.want)
		}
	}
	fragment := " {\n  \"inputs\": {\n  }\n }\t\r"
	if UnminifiedMetafile.MaybeRemoveWhitespace(fragment) != fragment || MinifiedMetafile.MaybeRemoveWhitespace(fragment) != "{\"inputs\":{}}\t\r" {
		t.Fatal("metafile formatting did not preserve payload punctuation and tab/CR")
	}
	for _, tt := range []struct {
		format Format
		want   string
	}{{FormatIIFE, "iife"}, {FormatCommonJS, "cjs"}, {FormatESModule, "esm"}, {FormatPreserve, ""}} {
		if got := tt.format.String(); got != tt.want {
			t.Fatalf("format label = %q, want %q", got, tt.want)
		}
	}
}
