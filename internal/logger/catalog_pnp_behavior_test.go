package logger_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

func TestCatalogNativePnPLegacyOctalErrorIdentifiesOriginalJSONToken(t *testing.T) {
	scratch, err := filepath.Abs("../../.tmp/compiler-fslogger-component")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0755); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(scratch, "native-pnp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	for _, test := range []struct{ name, escaped string }{
		{"plain", "b"}, {"two-digit", `\62`}, {"three-digit", `\142`}, {"maximum-three-digit", `\377`},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := `hydrateRuntimeState(JSON.parse('{"value":"a` + test.escaped + `", "after": }'));`
			for name, contents := range map[string]string{".pnp.cjs": manifest, "entry.js": `require("missing-package");`} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result := api.Build(api.BuildOptions{AbsWorkingDir: directory, EntryPoints: []string{"entry.js"}, Bundle: true, Write: false, LogLevel: api.LogLevelSilent})
			want := strings.LastIndex(manifest, "}")
			for _, message := range result.Errors {
				t.Logf("actual native Build error: %+v location=%+v", message, message.Location)
				if message.Location != nil && strings.HasSuffix(message.Location.File, ".pnp.cjs") && message.Text == "Unexpected \"}\" in JSON" {
					if message.Location.Line != 1 || message.Location.Column != want {
						t.Fatalf("native PnP JSON token remapped to column %d, want original token at %d", message.Location.Column, want)
					}
					return
				}
			}
			t.Fatalf("native PnP parser did not report the malformed embedded JSON token: %+v", result.Errors)
		})
	}
}
