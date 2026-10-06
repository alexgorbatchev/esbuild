package test_test

import (
	"strings"
	"testing"

	"github.com/evanw/esbuild/internal/config"
	"github.com/evanw/esbuild/internal/js_parser"
	"github.com/evanw/esbuild/internal/logger"
	"github.com/evanw/esbuild/internal/test"
)

func TestCatalogSourceForNativeParserDiagnostic(t *testing.T) {
	for _, tt := range []struct {
		name, contents string
		wantError      bool
	}{
		{"valid_multiline", "const value = 7;\nconsole.log(value);", false},
		{"invalid_second_line", "const good = 1;\nconst value = ;", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := test.SourceForTest(tt.contents)
			log := logger.NewDeferLog(logger.DeferLogAll, nil)
			_, ok := js_parser.Parse(log, source, js_parser.OptionsFromConfig(&config.Options{}))
			msgs := log.Done()
			if ok == tt.wantError || log.HasErrors() != tt.wantError {
				t.Fatalf("native parse success=%v, errors=%v: %+v", ok, log.HasErrors(), msgs)
			}
			if !tt.wantError {
				if len(msgs) != 0 {
					t.Fatalf("valid source produced diagnostics: %+v", msgs)
				}
				return
			}
			if len(msgs) != 1 || msgs[0].Kind != logger.Error || msgs[0].Data.Location == nil {
				t.Fatalf("invalid source lost its actual error location: %+v", msgs)
			}
			loc := msgs[0].Data.Location
			line := strings.Split(tt.contents, "\n")[1]
			if loc.File.Abs != "<stdin>" || loc.File.Rel != "<stdin>" || loc.Line != 2 || loc.LineText != line || loc.Column != strings.IndexByte(line, ';') || loc.Length != 1 {
				t.Fatalf("source identity/line/token mapping changed: %+v", loc)
			}
			rendered := msgs[0].String(logger.OutputOptions{IncludeSource: true, Color: logger.ColorNever}, logger.TerminalInfo{})
			if !strings.Contains(rendered, "<stdin>:2:") || !strings.Contains(rendered, line) || !strings.Contains(msgs[0].Data.Text, ";") {
				t.Fatalf("native diagnostic did not render the rejected source token: %s", rendered)
			}
		})
	}
}
