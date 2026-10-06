package sourcemap_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/evanw/esbuild/internal/ast"
	"github.com/evanw/esbuild/internal/helpers"
	"github.com/evanw/esbuild/internal/js_parser"
	"github.com/evanw/esbuild/internal/logger"
	"github.com/evanw/esbuild/internal/sourcemap"
)

type catalogSourceMapJSON struct {
	Version  int      `json:"version"`
	Sources  []string `json:"sources"`
	Names    []string `json:"names"`
	Mappings string   `json:"mappings"`
}

func catalogParseMappings(t *testing.T, mappings []byte, names []string) *sourcemap.SourceMap {
	t.Helper()
	contents, err := json.Marshal(catalogSourceMapJSON{Version: 3, Sources: []string{"one.ts", "two.ts", "three.ts"}, Names: names, Mappings: string(mappings)})
	if err != nil {
		t.Fatal(err)
	}
	log := logger.NewDeferLog(logger.DeferLogAll, nil)
	parsed := js_parser.ParseSourceMap(log, logger.Source{Contents: string(contents)})
	if messages := log.Done(); parsed == nil || len(messages) != 0 {
		t.Fatalf("native generated mappings did not parse: %q / %+v", mappings, messages)
	}
	return parsed
}

func catalogChunkNames(t *testing.T, quoted [][]byte) []string {
	t.Helper()
	names := make([]string, len(quoted))
	for i, name := range quoted {
		if err := json.Unmarshal(name, &names[i]); err != nil {
			t.Fatal(err)
		}
	}
	return names
}

func TestCatalogVLQNativePublishedSignedVectors(t *testing.T) {
	// ECMA-426 section6 publishes iB=17 and V=-10; single-digit values
	// below follow its explicit base64/sign grammar.
	for _, test := range []struct {
		encoded string
		value   int
	}{
		{"A", 0}, {"C", 1}, {"D", -1}, {"iB", 17}, {"V", -10}, {"B", -2147483648}, {"gggggggA", 0},
	} {
		t.Run(test.encoded, func(t *testing.T) {
			input := []byte("?!" + test.encoded + ",rest")
			if value, next := sourcemap.DecodeVLQ(input, 2); value != test.value || next != 2+len(test.encoded) {
				t.Errorf("native byte VLQ = %d,next%d; want %d,next%d", value, next, test.value, 2+len(test.encoded))
			}
			value, next, ok := sourcemap.DecodeVLQUTF16(utf16.Encode([]rune(test.encoded + ",rest")))
			if !ok || int(value) != test.value || next != len(test.encoded) {
				t.Errorf("native UTF16 VLQ = %d,next%d,ok%v", value, next, ok)
			}
		})
	}
	for _, encoded := range [][]uint16{nil, {'!'}, {'g'}} {
		if value, next, ok := sourcemap.DecodeVLQUTF16(encoded); ok || value != 0 || next != 0 {
			t.Fatalf("invalid/truncated UTF16 VLQ accepted: %v => %d,%d,%v", encoded, value, next, ok)
		}
	}
	for _, encoded := range []string{"ggggggE", "gggggggB"} {
		if value, next, ok := sourcemap.DecodeVLQUTF16(utf16.Encode([]rune(encoded))); ok || value != 0 || next != 0 {
			t.Fatalf("overflowing unsigned VLQ accepted: %q => %d,%d,%v", encoded, value, next, ok)
		}
	}
}

func TestCatalogNativeLargeColumnShiftRoundTripsThroughMapParser(t *testing.T) {
	for _, test := range []struct {
		name   string
		column int
	}{{"below signed bit", (1 << 30) - 1}, {"at signed bit", 1 << 30}, {"maximum column", (1 << 31) - 1}} {
		t.Run(test.name, func(t *testing.T) {
			// The native finalizer encodes the column using the package's encoder;
			// this checks its intended consumer, without a second VLQ implementation.
			pieces := sourcemap.SourceMapPieces{Mappings: []byte("CAAA")}
			encoded := pieces.Finalize([]sourcemap.SourceMapShift{{}, {
				Before: sourcemap.LineColumnOffset{}, After: sourcemap.LineColumnOffset{Columns: test.column - 1},
			}})
			parsed := catalogParseMappings(t, encoded, nil)
			mapping := parsed.Find(0, int32(test.column))
			if mapping == nil || mapping.GeneratedColumn != int32(test.column) {
				t.Fatalf("native positive column %d did not survive encoded map parsing: %+v", test.column, mapping)
			}
		})
	}
}

func TestCatalogNativeMinimumSignedDeltaUsesStandardVLQ(t *testing.T) {
	// Exercise the native chunk encoder at the signed boundary. The resulting
	// first original column is negative, so the map parser must reject it.
	chunk := sourcemap.MakeChunkBuilder(nil, sourcemap.GenerateLineOffsetTables("original", 1), false)
	chunk.AddSourceMapping(logger.Loc{}, "", nil)
	var encoded helpers.Joiner
	sourcemap.AppendSourceMapChunk(&encoded,
		sourcemap.SourceMapState{OriginalColumn: (1 << 31) - 1},
		sourcemap.SourceMapState{OriginalColumn: -1}, chunk.GenerateChunk(nil).Buffer)
	result := encoded.Done()
	if len(result) != 4 || result[3] != 'B' {
		t.Fatalf("native minimum signed delta did not use the standard B encoding: %q", result)
	}
	if value, next := sourcemap.DecodeVLQ(result, 3); value != -1<<31 || next != len(result) {
		t.Fatalf("native minimum signed delta byte roundtrip = %d,next%d", value, next)
	}
	if value, next, ok := sourcemap.DecodeVLQUTF16(utf16.Encode([]rune(string(result[3:])))); value != -1<<31 || next != 1 || !ok {
		t.Fatalf("native minimum signed delta UTF16 roundtrip = %d,next%d,ok%v", value, next, ok)
	}
	contents, err := json.Marshal(catalogSourceMapJSON{Version: 3, Sources: []string{"input.ts"}, Mappings: string(result)})
	if err != nil {
		t.Fatal(err)
	}
	log := logger.NewDeferLog(logger.DeferLogAll, nil)
	if parsed := js_parser.ParseSourceMap(log, logger.Source{Contents: string(contents)}); parsed != nil || len(log.Done()) == 0 {
		t.Fatal("native minimum signed delta became an accepted negative original position")
	}
}

func TestCatalogUTF16MappingInputRejectsNonBase64AndNegativeZero(t *testing.T) {
	for _, test := range []struct{ name, mappings string }{
		{"non-ASCII base64 alias", "\u0141AAA"}, {"negative-zero generated column", "BAAA"},
		{"overflowing generated column", "ggggggEAAA"}, {"overflowing continuation column", "gggggggBAAA"},
	} {
		t.Run(test.name, func(t *testing.T) {
			contents, err := json.Marshal(catalogSourceMapJSON{Version: 3, Sources: []string{"input.ts"}, Mappings: test.mappings})
			if err != nil {
				t.Fatal(err)
			}
			log := logger.NewDeferLog(logger.DeferLogAll, nil)
			parsed := js_parser.ParseSourceMap(log, logger.Source{Contents: string(contents)})
			if parsed != nil {
				t.Fatalf("native parser accepted invalid source-map generated position: %q => %+v", test.mappings, parsed.Mappings)
			}
			if len(log.Done()) == 0 {
				t.Fatal("native parser rejected external mappings without its diagnostic")
			}
		})
	}
}

func TestCatalogNativeMapFindPreservesLineAndPrecedingColumnIdentity(t *testing.T) {
	mappings := []sourcemap.Mapping{
		{GeneratedLine: 0, GeneratedColumn: 2, OriginalLine: 7},
		{GeneratedLine: 0, GeneratedColumn: 9, OriginalLine: 8},
		{GeneratedLine: 2, GeneratedColumn: 0, OriginalLine: 9},
		{GeneratedLine: 2, GeneratedColumn: 4, OriginalLine: 10},
	}
	mapData := sourcemap.SourceMap{Mappings: mappings}
	for _, test := range []struct {
		line, column int32
		index        int
	}{
		{0, 0, -1}, {0, 2, 0}, {0, 8, 0}, {0, 9, 1}, {0, 100, 1},
		{1, 100, -1}, {2, 0, 2}, {2, 3, 2}, {2, 4, 3}, {3, 0, -1},
	} {
		actual := mapData.Find(test.line, test.column)
		if test.index < 0 {
			if actual != nil {
				t.Fatalf("unmapped line/column acquired a preceding-line mapping: %+v", actual)
			}
		} else if actual != &mappings[test.index] {
			t.Fatalf("native source-map search lost exact mapping identity: %d:%d => %+v", test.line, test.column, actual)
		}
	}
	if empty := (sourcemap.SourceMap{}); empty.Find(0, 0) != nil {
		t.Fatal("empty source map fabricated a mapping")
	}
}

func TestCatalogNativeOffsetsCountUTF16AndLineTransitions(t *testing.T) {
	for _, test := range []struct {
		text string
		want sourcemap.LineColumnOffset
	}{
		{"ASCII", sourcemap.LineColumnOffset{Columns: 5}}, {"a👋α", sourcemap.LineColumnOffset{Columns: 4}},
		{"a\r\nα👋", sourcemap.LineColumnOffset{Lines: 1, Columns: 3}},
		{"a\rb\nc\u2028d\u2029", sourcemap.LineColumnOffset{Lines: 4}},
	} {
		var fromBytes, fromString sourcemap.LineColumnOffset
		fromBytes.AdvanceBytes([]byte(test.text))
		fromString.AdvanceString(test.text)
		if fromBytes != test.want || fromString != test.want {
			t.Fatalf("native text offset %q = bytes%+v/string%+v, want %+v", test.text, fromBytes, fromString, test.want)
		}
	}
	offset := sourcemap.LineColumnOffset{Lines: 1, Columns: 4}
	offset.Add(sourcemap.LineColumnOffset{Columns: 3})
	if offset != (sourcemap.LineColumnOffset{Lines: 1, Columns: 7}) {
		t.Fatal("same-line fragment did not preserve the accumulated column")
	}
	offset.Add(sourcemap.LineColumnOffset{Lines: 2, Columns: 5})
	if offset != (sourcemap.LineColumnOffset{Lines: 3, Columns: 5}) || offset.ComesBefore(offset) || !offset.ComesBefore(sourcemap.LineColumnOffset{Lines: 4}) || !offset.ComesBefore(sourcemap.LineColumnOffset{Lines: 3, Columns: 6}) {
		t.Fatal("native multi-line fragment did not reset its column or preserve strict ordering")
	}
}

func TestCatalogNativeChunkBuilderRemapsUTF16AndCoversUnmappedLines(t *testing.T) {
	input := "a👋b\r\nαx\nz"
	for _, asciiOnly := range []bool{false, true} {
		builder := sourcemap.MakeChunkBuilder(nil, sourcemap.GenerateLineOffsetTables(input, 1), asciiOnly)
		var output []byte
		builder.AddSourceMapping(logger.Loc{}, "α", output)
		first := bytes.Clone(builder.GenerateChunk(output).Buffer.Data)
		builder.AddSourceMapping(logger.Loc{}, "α", output)
		if !bytes.Equal(builder.GenerateChunk(output).Buffer.Data, first) {
			t.Fatal("duplicate native location added another source mapping")
		}
		output = append(output, []byte("a👋")...)
		builder.AddSourceMapping(logger.Loc{Start: int32(strings.Index(input, "b"))}, "", output)
		output = append(output, []byte("\r\n__")...)
		builder.AddSourceMapping(logger.Loc{Start: int32(strings.Index(input, "α"))}, "α", output)
		output = append(output, []byte("\n\nQ")...)
		builder.AddSourceMapping(logger.Loc{Start: int32(strings.Index(input, "z"))}, "", output)
		chunk := builder.GenerateChunk(output)
		names := catalogChunkNames(t, chunk.QuotedNames)
		if chunk.ShouldIgnore || chunk.FinalGeneratedColumn != 1 || !reflect.DeepEqual(names, []string{"α"}) {
			t.Fatalf("native chunk lost its mapping/name/end position: %+v / %v", chunk, names)
		}
		if asciiOnly && bytes.Contains(chunk.QuotedNames[0], []byte("α")) {
			t.Fatal("ASCII output retained an unescaped source name")
		}
		parsed := catalogParseMappings(t, chunk.Buffer.Data, names)
		for _, test := range []struct{ line, column, originalLine, originalColumn int32 }{
			{0, 0, 0, 0}, {0, 3, 0, 3}, {1, 0, 0, 3}, {1, 2, 1, 0},
			{2, 0, 1, 0}, {3, 0, 1, 0}, {3, 1, 2, 0},
		} {
			mapping := parsed.Find(test.line, test.column)
			if mapping == nil || mapping.OriginalLine != test.originalLine || mapping.OriginalColumn != test.originalColumn {
				t.Fatalf("native UTF16/line remapping %d:%d = %+v", test.line, test.column, mapping)
			}
		}
	}
}

func TestCatalogNativeNestedMapPreservesOriginalNamesAndMissingPositions(t *testing.T) {
	inputMap := &sourcemap.SourceMap{Names: []string{"original-α"}, Mappings: []sourcemap.Mapping{
		{GeneratedLine: 0, SourceIndex: 1, OriginalLine: 8, OriginalColumn: 13, OriginalName: ast.MakeIndex32(0)},
		{GeneratedLine: 1, SourceIndex: 0, OriginalLine: 9, OriginalColumn: 2},
	}}
	input := "one\ntwo\nthree"
	builder := sourcemap.MakeChunkBuilder(inputMap, sourcemap.GenerateLineOffsetTables(input, 1), false)
	var output []byte
	for _, test := range []struct{ text, original, name string }{
		{"", "one", "intermediate"}, {"\n_", "two", "fallback"}, {"\n_", "three", "unmapped"},
	} {
		output = append(output, []byte(test.text)...)
		builder.AddSourceMapping(logger.Loc{Start: int32(strings.Index(input, test.original))}, test.name, output)
	}
	chunk := builder.GenerateChunk(output)
	names := catalogChunkNames(t, chunk.QuotedNames)
	parsed := catalogParseMappings(t, chunk.Buffer.Data, names)
	first, second := parsed.Find(0, 0), parsed.Find(1, 1)
	if first == nil || first.SourceIndex != 1 || first.OriginalLine != 8 || first.OriginalColumn != 13 || !first.OriginalName.IsValid() || names[first.OriginalName.GetIndex()] != "original-α" {
		t.Fatalf("nested native map lost its original location/name: %+v / %v", first, names)
	}
	if second == nil || second.SourceIndex != 0 || second.OriginalLine != 9 || second.OriginalColumn != 2 || !second.OriginalName.IsValid() || names[second.OriginalName.GetIndex()] != "fallback" || parsed.Find(2, 1) != nil {
		t.Fatalf("native nested map invented an unmapped position or lost fallback name: %+v", second)
	}
	empty := sourcemap.MakeChunkBuilder(&sourcemap.SourceMap{}, sourcemap.GenerateLineOffsetTables(input, 1), false)
	empty.AddSourceMapping(logger.Loc{}, "missing", nil)
	if !empty.GenerateChunk([]byte("\n")).ShouldIgnore {
		t.Fatal("a native chunk with no accepted original mappings became meaningful")
	}
}

func TestCatalogNativeSourceMapFinalizationShiftsOnlyFollowingMappings(t *testing.T) {
	input := "zero\none\ntwo\nthree"
	builder := sourcemap.MakeChunkBuilder(nil, sourcemap.GenerateLineOffsetTables(input, 1), false)
	var output []byte
	for _, test := range []struct{ generated, original string }{
		{"", "zero"}, {"xxxx", "one"}, {"xxxxx", "two"}, {"\nabcde", "three"},
	} {
		output = append(output, []byte(test.generated)...)
		builder.AddSourceMapping(logger.Loc{Start: int32(strings.Index(input, test.original))}, "", output)
	}
	chunk := builder.GenerateChunk(output)
	pieces := sourcemap.SourceMapPieces{Prefix: []byte("prefix:"), Mappings: chunk.Buffer.Data, Suffix: []byte(":suffix")}
	if !pieces.HasContent() {
		t.Fatal("a generated native mapping buffer was considered empty")
	}
	for _, capacity := range []int{len(pieces.Prefix), len(pieces.Prefix) + len(pieces.Mappings) + len(pieces.Suffix)} {
		prefix := make([]byte, len(pieces.Prefix), capacity)
		copy(prefix, pieces.Prefix)
		fast := pieces
		fast.Prefix = prefix
		want := append(append(bytes.Clone(pieces.Prefix), pieces.Mappings...), pieces.Suffix...)
		if finalized := fast.Finalize([]sourcemap.SourceMapShift{{}}); !bytes.Equal(finalized, want) {
			t.Fatal("native fast finalization lost prefix, mappings or suffix")
		}
	}
	shifts := []sourcemap.SourceMapShift{
		{}, {Before: sourcemap.LineColumnOffset{Columns: 3}, After: sourcemap.LineColumnOffset{Columns: 6}},
		{Before: sourcemap.LineColumnOffset{Columns: 7}, After: sourcemap.LineColumnOffset{Columns: 8}},
		{Before: sourcemap.LineColumnOffset{Columns: 11}, After: sourcemap.LineColumnOffset{Columns: 20}},
		{Before: sourcemap.LineColumnOffset{Lines: 1, Columns: 2}, After: sourcemap.LineColumnOffset{Lines: 1, Columns: 7}},
	}
	finalized := pieces.Finalize(shifts)
	parsed := catalogParseMappings(t, finalized[len(pieces.Prefix):len(finalized)-len(pieces.Suffix)], nil)
	for _, test := range []struct{ line, column, originalLine int32 }{{0, 0, 0}, {0, 7, 1}, {0, 10, 2}, {1, 0, 2}, {1, 10, 3}} {
		mapping := parsed.Find(test.line, test.column)
		if mapping == nil || mapping.GeneratedColumn != test.column || mapping.OriginalLine != test.originalLine {
			t.Fatalf("native shifted source mapping %d:%d = %+v", test.line, test.column, mapping)
		}
	}
}

func TestCatalogNativeChunkJoiningPreservesRelativeSourcesAndNames(t *testing.T) {
	for _, test := range []struct {
		name, leading string
		start         sourcemap.SourceMapState
		line, column  int32
		withNames     bool
	}{
		{"same line", "", sourcemap.SourceMapState{GeneratedColumn: 2, SourceIndex: 1, OriginalName: 1}, 0, 2, true},
		{"new lines", "", sourcemap.SourceMapState{GeneratedLine: 2, SourceIndex: 1, OriginalName: 1}, 2, 0, true},
		{"leading separator", "\n", sourcemap.SourceMapState{GeneratedColumn: 13, SourceIndex: 1}, 1, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := sourcemap.MakeChunkBuilder(nil, sourcemap.GenerateLineOffsetTables("first", 1), false)
			first.AddSourceMapping(logger.Loc{}, "one", nil)
			one := first.GenerateChunk([]byte("xx"))
			second := sourcemap.MakeChunkBuilder(nil, sourcemap.GenerateLineOffsetTables("second", 1), false)
			name := ""
			if test.withNames {
				name = "two"
			}
			second.AddSourceMapping(logger.Loc{}, name, []byte(test.leading))
			two := second.GenerateChunk([]byte(test.leading + "z"))
			var joined helpers.Joiner
			sourcemap.AppendSourceMapChunk(&joined, sourcemap.SourceMapState{}, sourcemap.SourceMapState{}, one.Buffer)
			sourcemap.AppendSourceMapChunk(&joined, one.EndState, test.start, two.Buffer)
			parsed := catalogParseMappings(t, joined.Done(), []string{"one", "two"})
			mapping := parsed.Find(test.line, test.column)
			if mapping == nil || mapping.SourceIndex != 1 || mapping.OriginalLine != 0 || mapping.OriginalColumn != 0 || mapping.GeneratedColumn != test.column {
				t.Fatalf("native joined chunk lost relative source/position: %+v", mapping)
			}
			if test.withNames && (!mapping.OriginalName.IsValid() || parsed.Names[mapping.OriginalName.GetIndex()] != "two") {
				t.Fatalf("native joined chunk lost its relative original name: %+v", mapping)
			}
		})
	}
}
