package helpers_test

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/internal/helpers"
	"github.com/evanw/esbuild/internal/logger"
)

func TestCatalogUnicodeConversion(t *testing.T) {
	for _, input := range []string{"", "plain", "é水", "🦊𐐀", "a🦊z"} {
		t.Run(input, func(t *testing.T) {
			units := helpers.StringToUTF16(input)
			if !reflect.DeepEqual(units, utf16.Encode([]rune(input))) && !(len(units) == 0 && input == "") {
				t.Fatalf("UTF-16 conversion = %x", units)
			}
			text, invalid, valid := helpers.UTF16ToStringWithValidation(units)
			if !valid || invalid != 0 || text != input || helpers.UTF16ToString(units) != input || !helpers.UTF16EqualsString(units, input) {
				t.Fatalf("Unicode round trip = %q / %x / %v", text, invalid, valid)
			}
			if helpers.ContainsNonBMPCodePointUTF16(units) != helpers.ContainsNonBMPCodePoint(input) {
				t.Fatal("UTF-16 supplementary-character detection differs from decoded text")
			}
		})
	}
	for _, tt := range []struct {
		units []uint16
		bad   uint16
	}{
		{[]uint16{0xD800}, 0xD800}, {[]uint16{'a', 0xDC00}, 0xDC00}, {[]uint16{0xDBFF, 'x'}, 0xDBFF},
	} {
		if text, bad, valid := helpers.UTF16ToStringWithValidation(tt.units); valid || text != "" || bad != tt.bad {
			t.Fatalf("unpaired surrogate %x accepted: %q / %x / %v", tt.units, text, bad, valid)
		}
		if helpers.ContainsNonBMPCodePointUTF16(tt.units) {
			t.Fatalf("unpaired surrogate %x treated as a supplementary code point", tt.units)
		}
	}
	// WTF-8 retains unpaired surrogate code units instead of replacing them.
	if got := helpers.UTF16ToString([]uint16{0xD800, 'x', 0xDFFF}); got != "\xed\xa0\x80x\xed\xbf\xbf" {
		t.Fatalf("unpaired-surrogate preservation = %x", got)
	}
}

func TestCatalogWTF8DecodeBoundaries(t *testing.T) {
	for _, input := range []string{"A", "é", "水", "🦊", "\U0010FFFF"} {
		want, width := utf8.DecodeRuneInString(input)
		if got, n := helpers.DecodeWTF8Rune(input); got != want || n != width {
			t.Fatalf("decode %x = %U / %d, want %U / %d", input, got, n, want, width)
		}
	}
	for _, tt := range []struct {
		input string
		want  rune
		width int
	}{
		{"", utf8.RuneError, 0}, {"\xc2", utf8.RuneError, 0}, {"\xe1\x80", utf8.RuneError, 0}, {"\xf0\x90\x80", utf8.RuneError, 0},
		{"\x80", utf8.RuneError, 1}, {"\xff", utf8.RuneError, 1}, {"\xc2A", utf8.RuneError, 1},
		{"\xc0\x80", utf8.RuneError, 1}, {"\xe0\x80\x80", utf8.RuneError, 1}, {"\xe1\x80A", utf8.RuneError, 1},
		{"\xf0A\x80\x80", utf8.RuneError, 1}, {"\xf0\x90A\x80", utf8.RuneError, 1}, {"\xf0\x90\x80A", utf8.RuneError, 1},
		{"\xf0\x80\x80\x80", utf8.RuneError, 1}, {"\xf4\x90\x80\x80", utf8.RuneError, 1},
		{"\xed\xa0\x80", 0xD800, 3}, {"\xed\xbf\xbf", 0xDFFF, 3},
	} {
		if got, n := helpers.DecodeWTF8Rune(tt.input); got != tt.want || n != tt.width {
			t.Fatalf("decode %x = %U / %d, want %U / %d", tt.input, got, n, tt.want, tt.width)
		}
	}
}

func TestCatalogQuotedTextRoundTrips(t *testing.T) {
	inputs := []string{"", "plain", "it's", "'", `double" slash\`, "\b\f\n\r\t\x00\x1f", "é水🦊", "\uFEFF"}
	for _, asciiOnly := range []bool{false, true} {
		for _, input := range inputs {
			t.Run(input+"/"+map[bool]string{false: "unicode", true: "ascii"}[asciiOnly], func(t *testing.T) {
				quoted := helpers.QuoteForJSON(input, asciiOnly)
				var decoded string
				if err := json.Unmarshal(quoted, &decoded); err != nil || decoded != input {
					t.Fatalf("JSON quote %q did not round trip: %s / %q / %v", input, quoted, decoded, err)
				}
				single := helpers.QuoteSingle(input, asciiOnly)
				value, err := goja.New().RunString(string(single))
				if err != nil {
					t.Fatalf("single-quoted text %q rejected by actual JS runtime: %s / %v", input, single, err)
				}
				if got := value.String(); got != input {
					t.Fatalf("single quote round trip = %q, want %q", got, input)
				}
				if asciiOnly {
					for _, text := range [][]byte{quoted, single} {
						for _, b := range text {
							if b > 0x7F {
								t.Fatalf("ASCII-only quote contains byte %x", b)
							}
						}
					}
				}
			})
		}
	}
	if got := string(helpers.QuoteForJSON("\xed\xa0\x80", false)); got != `"\uD800"` {
		t.Fatalf("WTF-8 surrogate JSON representation = %s", got)
	}
}

func TestCatalogThreadSafeWaitGroup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wg := helpers.MakeThreadSafeWaitGroup()
		wg.Add(1)
		finished := false
		go func() { wg.Wait(); finished = true }()
		synctest.Wait()
		if finished {
			t.Fatal("wait completed before initial work")
		}
		wg.Add(1)
		wg.Done()
		synctest.Wait()
		if finished {
			t.Fatal("wait ignored work added while waiting")
		}
		wg.Done()
		synctest.Wait()
		if !finished {
			t.Fatal("wait did not complete after all work")
		}
	})
	defer func() {
		if got := recover(); got != "sync: negative WaitGroup counter" {
			t.Fatalf("negative counter panic = %v", got)
		}
	}()
	helpers.MakeThreadSafeWaitGroup().Done()
}

func TestCatalogTimerProducesNestedDiagnostics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var timer helpers.Timer
		timer.Begin("build")
		child := timer.Fork()
		child.Begin("parse")
		time.Sleep(2 * time.Millisecond)
		child.End("parse")
		timer.Join(child)
		time.Sleep(time.Millisecond)
		timer.End("build")
		log := logger.NewDeferLog(logger.DeferLogAll, nil)
		timer.Log(log)
		msgs := log.Done()
		if len(msgs) != 1 || msgs[0].Kind != logger.Info || len(msgs[0].Notes) != 2 || msgs[0].Notes[0].Text != "build: 3ms" || msgs[0].Notes[1].Text != "  parse: 2ms" {
			t.Fatalf("nested timing diagnostic = %+v", msgs)
		}
		var disabled *helpers.Timer
		disabled.Begin("ignored")
		disabled.End("ignored")
		disabled.Join(&timer)
		disabled.Log(log)
		if disabled.Fork() != nil {
			t.Fatal("disabled timer created a child")
		}
		timer.Join(nil)
	})
	defer func() {
		if recover() == nil {
			t.Fatal("misnested timer did not reject wrong end name")
		}
	}()
	var timer helpers.Timer
	timer.Begin("parse")
	timer.End("build")
	timer.Log(logger.NewDeferLog(logger.DeferLogAll, nil))
}

func TestCatalogStackRetainsCallSites(t *testing.T) {
	stack := helpers.PrettyPrintedStack()
	if !strings.Contains(stack, "helpers_test.TestCatalogStackRetainsCallSites") || !strings.Contains(stack, "catalog_behavior_test.go:") {
		t.Fatalf("formatted native stack lost caller/source location: %s", stack)
	}
	if regexp.MustCompile(`(?m)^goroutine [0-9]+ \[`).MatchString(stack) || strings.Contains(stack, " +0x") {
		t.Fatalf("formatted stack retains runtime header/PC offsets: %s", stack)
	}
}
