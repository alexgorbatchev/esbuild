package logger

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests are deliberately sequential: stderr is a process-owned stream.
// The native pipe is drained concurrently and joined even if the callback exits.
func catalogCaptureStderr(t *testing.T, callback func()) (text string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(reader)
		done <- result{string(data), err}
	}()
	previous := os.Stderr
	os.Stderr = writer
	defer func() {
		os.Stderr = previous
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
		captured := <-done
		if captured.err != nil {
			t.Error(captured.err)
		}
		text = captured.text
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}()
	callback()
	return
}

func TestCatalogSourceLocationsScanBackwardAcrossNativeLineEndings(t *testing.T) {
	source := Source{
		Contents:    "first\r\n\tαβ\nthird\u2028fourth\u2029last",
		PrettyPaths: PrettyPaths{Abs: "/project/input.ts", Rel: "input.ts"},
	}
	tracker := MakeLineColumnTracker(&source)
	for _, test := range []struct {
		token, lineText string
		line, column    int
	}{{"last", "last", 5, 0}, {"β", "\tαβ", 2, 3}, {"fourth", "fourth", 4, 0}, {"first", "first", 1, 0}, {"third", "third", 3, 0}, {"α", "\tαβ", 2, 1}} {
		offset := strings.Index(source.Contents, test.token)
		rangeValue := Range{Loc: Loc{Start: int32(offset)}, Len: int32(len(test.token))}
		location := tracker.MsgLocationOrNil(rangeValue)
		if location == nil || location.File != source.PrettyPaths || location.Line != test.line || location.Column != test.column || location.LineText != test.lineText || location.Length != len(test.token) {
			t.Fatalf("source token %q lost its original byte location: %+v", test.token, location)
		}
		if got := source.TextForRange(rangeValue); got != test.token {
			t.Fatalf("source range extracted %q instead of %q", got, test.token)
		}
	}
	withoutSource := MakeLineColumnTracker(nil)
	if withoutSource.MsgLocationOrNil(Range{}) != nil || (*LineColumnTracker)(nil).MsgLocationOrNil(Range{}) != nil {
		t.Fatal("diagnostic without a source acquired an invented location")
	}
}

func TestCatalogSourceRangesPreserveEscapesAndCommentIndent(t *testing.T) {
	for _, test := range []struct {
		contents, expected string
	}{
		{`'a\'b' trailing`, `'a\'b'`}, {`"a\"b" trailing`, `"a\"b"`},
		{"`a\\`b` trailing", "`a\\`b`"}, {"`a${value}`", ""}, {"'unterminated", ""}, {"", ""},
	} {
		source := Source{Contents: test.contents}
		if got := source.TextForRange(source.RangeOfString(Loc{})); got != test.expected {
			t.Fatalf("string source span %q: %q, want %q", test.contents, got, test.expected)
		}
	}
	source := Source{Contents: "a += 0xF_F; \\123x \t\r\n"}
	number := source.RangeOfNumber(Loc{Start: int32(strings.Index(source.Contents, "0x"))})
	if source.TextForRange(number) != "0xF_F" || source.RangeOfNumber(Loc{}).Len != 0 {
		t.Fatal("numeric source span consumed punctuation or a non-number")
	}
	escape := source.RangeOfLegacyOctalEscape(Loc{Start: int32(strings.Index(source.Contents, `\123`))})
	if source.TextForRange(escape) != `\123` || source.RangeOfLegacyOctalEscape(Loc{}).Len != 0 {
		t.Fatal("legacy escape source range lost its original spelling")
	}
	if source.TextForRange(source.RangeOfOperatorBefore(number.Loc, "+=")) != "+=" || source.TextForRange(source.RangeOfOperatorAfter(Loc{}, "+=")) != "+=" {
		t.Fatal("operator range no longer identifies the original operator")
	}
	if source.RangeOfOperatorBefore(Loc{}, "missing").Len != 0 || source.RangeOfOperatorAfter(Loc{}, "missing").Len != 0 {
		t.Fatal("missing operator acquired a fabricated span")
	}
	trimmed := source.LocBeforeWhitespace(Loc{Start: int32(len(source.Contents))})
	if source.Contents[trimmed.Start-1] != 'x' {
		t.Fatalf("whitespace trim stopped at %d", trimmed.Start)
	}
	comment := Source{Contents: "\t\t/* first\r\n\t\tsecond\u2028\t\tthird\u2029\t\t*/"}
	rangeValue := Range{Loc: Loc{Start: 2}, Len: int32(len(comment.Contents) - 2)}
	if got := comment.CommentTextWithoutIndent(rangeValue); got != "/* first\nsecond\nthird\n*/" {
		t.Fatalf("comment indentation/newline normalization: %q", got)
	}
	if source.CommentTextWithoutIndent(number) != "0xF_F" {
		t.Fatal("ordinary source span was processed as an indented comment")
	}
	var union Range
	union.ExpandBy(number)
	union.ExpandBy(escape)
	if union.Loc != number.Loc || union.End() != escape.End() {
		t.Fatalf("source range union lost one endpoint: %+v", union)
	}
}

func TestCatalogDeferredMessagesRetainIdentityOverridesAndSortOrder(t *testing.T) {
	source := Source{Contents: "a\nb", PrettyPaths: PrettyPaths{Abs: "/p/input.ts", Rel: "input.ts"}}
	tracker := MakeLineColumnTracker(&source)
	log := NewDeferLog(DeferLogNoVerboseOrDebug, map[MsgID]LogLevel{MsgID_JS_DirectEval: LevelSilent, MsgID_JS_DuplicateCase: LevelError})
	log.AddID(MsgID_JS_DirectEval, Warning, &tracker, Range{}, "suppressed")
	log.AddIDWithNotes(MsgID_JS_DuplicateCase, Warning, &tracker, Range{Loc: Loc{Start: 2}, Len: 1}, "promoted", []MsgData{{Text: "note survives"}})
	log.AddMsg(Msg{Kind: Debug, Data: MsgData{Text: "hidden debug"}})
	log.AddMsg(Msg{Kind: Verbose, Data: MsgData{Text: "hidden verbose"}})
	log.AddError(&tracker, Range{Len: 1}, "first location")
	log.AddErrorWithNotes(&tracker, Range{}, "zero length", []MsgData{{Text: "extra"}})
	log.AddMsgID(MsgID_None, Msg{Kind: Info, Data: MsgData{Text: "no location", UserDetail: "user payload"}})
	if !log.HasErrors() {
		t.Fatal("promoted/native errors were not recorded")
	}
	peek := log.Peek()
	if len(peek) != 4 {
		t.Fatalf("filtering lost or retained the wrong messages: %v", peek)
	}
	peek[0].Data.Text = "caller mutation"
	if log.Peek()[0].Data.Text == "caller mutation" {
		t.Fatal("Peek exposed the logger's mutable storage")
	}
	messages := log.Done()
	if messages[0].Data.Text != "no location" || messages[1].Data.Text != "first location" || messages[3].Data.Text != "promoted" || messages[3].Kind != Error || messages[3].ID != MsgID_JS_DuplicateCase || messages[3].Notes[0].Text != "note survives" {
		t.Fatalf("deterministic diagnostic order/metadata changed: %+v", messages)
	}
	for _, level := range []LogLevel{LevelVerbose, LevelDebug, LevelInfo, LevelWarning, LevelError, LevelSilent} {
		log := NewDeferLog(DeferLogAll, map[MsgID]LogLevel{MsgID_JS_DuplicateCase: level})
		log.AddMsgID(MsgID_JS_DuplicateCase, Msg{Kind: Warning, Data: MsgData{Text: "override"}})
		messages := log.Done()
		if level == LevelSilent {
			if len(messages) != 0 {
				t.Fatal("silent ID override retained a diagnostic")
			}
		} else {
			wantKind := map[LogLevel]MsgKind{LevelVerbose: Verbose, LevelDebug: Debug, LevelInfo: Info, LevelWarning: Warning, LevelError: Error}[level]
			if len(messages) != 1 || messages[0].Kind != wantKind {
				t.Fatalf("native override did not change emitted severity: %v", messages)
			}
		}
	}
	concurrent := NewDeferLog(DeferLogAll, nil)
	var producers sync.WaitGroup
	for index := range 12 {
		producers.Go(func() { concurrent.AddMsg(Msg{Kind: Warning, Data: MsgData{Text: fmt.Sprint(index)}}) })
	}
	producers.Wait()
	if len(concurrent.Done()) != 12 || concurrent.HasErrors() {
		t.Fatal("concurrent warning collection lost messages or introduced an error")
	}
}

func TestCatalogDiagnosticFormattingClipsAndPreservesOriginalLocations(t *testing.T) {
	location := &MsgLocation{File: PrettyPaths{Abs: "/project/input.ts", Rel: "input.ts"}, Namespace: "file", Line: 2, Column: 2, Length: 4, LineText: "\t value\nnext line", Suggestion: "replacement"}
	message := Msg{Kind: Warning, ID: MsgID_JS_DuplicateCase, PluginName: "catalog-input", Data: MsgData{Text: "diagnostic", Location: location}, Notes: []MsgData{{Text: "more details"}, {Text: "read https://example.invalid/docs."}}}
	plain := message.String(OutputOptions{PathStyle: RelPath}, TerminalInfo{})
	if plain != "input.ts: WARNING: diagnostic\nNOTE: more details\nNOTE: read https://example.invalid/docs.\n" {
		t.Fatalf("source-free diagnostics changed contract: %q", plain)
	}
	absolute := message.String(OutputOptions{PathStyle: AbsPath}, TerminalInfo{})
	if !strings.HasPrefix(absolute, "/project/input.ts: WARNING:") {
		t.Fatalf("absolute diagnostic path was lost: %q", absolute)
	}
	formatted := message.String(OutputOptions{IncludeSource: true}, TerminalInfo{Width: 38})
	for _, text := range []string{"[WARNING]", "[plugin catalog-input]", "[duplicate-case]", "input.ts:2:2", "~~~~", "replacement", "next line", "more details"} {
		if !strings.Contains(formatted, text) {
			t.Fatalf("source diagnostic omitted %q: %q", text, formatted)
		}
	}
	if location.Column != 2 || location.Length != 4 || location.LineText != "\t value\nnext line" {
		t.Fatal("formatting mutated caller's original source location")
	}
	for _, kind := range []MsgKind{Error, Warning, Info, Debug, Verbose} {
		colored := (Msg{Kind: kind, Data: MsgData{Text: "colored"}}).String(OutputOptions{IncludeSource: true}, TerminalInfo{UseColorEscapes: true})
		if !strings.Contains(colored, "\x1b[") || !strings.Contains(colored, kind.String()) {
			t.Fatalf("colored severity lost formatting: %q", colored)
		}
	}
	longLine := strings.Repeat("a", 40) + "MARK" + strings.Repeat("z", 40)
	detail := detailStruct(MsgData{Location: &MsgLocation{File: location.File, Line: 9, Column: 40, Length: 4, LineText: longLine}}, RelPath, TerminalInfo{Width: 32}, 1)
	if detail.SourceMarked != "MARK" || detail.Marker != "~~~~" || !strings.Contains(detail.SourceBefore, "...") || !strings.Contains(detail.SourceAfter, "...") {
		t.Fatalf("terminal clipping lost marked source or ellipses: %+v", detail)
	}
	detail = detailStruct(MsgData{Location: &MsgLocation{File: location.File, Line: -1, Column: -1, Length: -1, LineText: "text"}}, AbsPath, TerminalInfo{}, 1)
	if detail.Line != 0 || detail.Column != 0 || detail.Marker != "^" || detail.Path != location.File.Abs {
		t.Fatalf("external diagnostic geometry was not clamped safely: %+v", detail)
	}
	note := (Msg{Kind: Note, Data: MsgData{Text: "one two three four five"}}).String(OutputOptions{IncludeSource: true}, TerminalInfo{Width: 10})
	if note != "  one two\n  three\n  four\n  five\n\n" {
		t.Fatalf("note wrapping lost words or terminal width: %q", note)
	}
	linked := linkifyText("read https://example.invalid/a, then https://example.invalid/b.", "<u>", "</u>")
	if linked != "read <u>https://example.invalid/a</u>, then <u>https://example.invalid/b</u>." {
		t.Fatalf("link formatting consumed punctuation: %q", linked)
	}
}

func TestCatalogNativeStderrReservesErrorSlotsAndRetainsCompleteMessages(t *testing.T) {
	var messages []Msg
	text := catalogCaptureStderr(t, func() {
		log := NewStderrLog(OutputOptions{Color: ColorNever, LogLevel: LevelInfo, MessageLimit: 2})
		log.AddMsg(Msg{Kind: Warning, Data: MsgData{Text: "first warning"}})
		log.AddMsg(Msg{Kind: Warning, Data: MsgData{Text: "deferred warning"}})
		log.AddMsg(Msg{Kind: Debug, Data: MsgData{Text: "hidden debug"}})
		log.AddMsg(Msg{Kind: Info, Data: MsgData{Text: "visible info"}})
		log.AddMsg(Msg{Kind: Error, Data: MsgData{Text: "reserved error"}})
		if !log.HasErrors() || len(log.Peek()) != 5 {
			t.Error("bounded presentation discarded complete diagnostic state")
		}
		messages = log.Done()
	})
	if !strings.Contains(text, "first warning") || !strings.Contains(text, "reserved error") || !strings.Contains(text, "visible info") || strings.Contains(text, "deferred warning\n") || strings.Contains(text, "hidden debug") || !strings.Contains(text, "1 of 2 warnings and 1 error shown") || len(messages) != 5 {
		t.Fatalf("bounded native diagnostic output/state changed: %q / %v", text, messages)
	}
	for _, level := range []LogLevel{LevelSilent, LevelVerbose} {
		text := catalogCaptureStderr(t, func() {
			log := NewStderrLog(OutputOptions{Color: ColorNever, LogLevel: level, MessageLimit: 1})
			log.AddMsg(Msg{Kind: Warning, Data: MsgData{Text: "queued warning"}})
			log.AddMsg(Msg{Kind: Debug, Data: MsgData{Text: "debug"}})
			log.AddMsg(Msg{Kind: Verbose, Data: MsgData{Text: "verbose"}})
			log.Done()
		})
		if level == LevelSilent && text != "" || level == LevelVerbose && (!strings.Contains(text, "queued warning") || !strings.Contains(text, "debug") || !strings.Contains(text, "verbose")) {
			t.Fatalf("native log level/deferred warning output: %q", text)
		}
	}
	text = catalogCaptureStderr(t, func() {
		PrintErrorWithNoteToStderr([]string{"--color=false"}, "failure", "explanation")
		PrintErrorToStderr([]string{"--color=false"}, "second failure")
	})
	if !strings.Contains(text, "failure") || !strings.Contains(text, "explanation") || !strings.Contains(text, "second failure") || strings.Contains(text, "\x1b[") {
		t.Fatalf("native error helpers lost notes or explicit color selection: %q", text)
	}
}

func TestCatalogNativeSummaryAndTextFiltering(t *testing.T) {
	table := SummaryTable{{Dir: "out/", Base: "small.js", Size: "1kb", Bytes: 1024}, {Dir: "out/", Base: "large.js.map", Size: "2mb", Bytes: 2 * 1024 * 1024, IsSourceMap: true}, {Dir: strings.Repeat("directory/", 12), Base: "large.js", Size: "1mb", Bytes: 1024 * 1024}}
	start := time.Now()
	text := catalogCaptureStderr(t, func() { PrintSummary(ColorNever, table, &start) })
	if table[0].Base != "large.js" || table[len(table)-1].Base != "large.js.map" || !strings.Contains(text, "...") || !strings.Contains(text, "small.js") || !strings.Contains(text, "large.js.map") || !strings.Contains(text, "Done in ") || strings.Contains(text, "\x1b[") {
		t.Fatalf("native summary sorting/clipping/color/timing changed: %q / %v", text, table)
	}
	for index := range 23 {
		table = append(table, SummaryTableEntry{Base: fmt.Sprintf("asset-%02d.js", index), Size: "1b", Bytes: 1})
	}
	text = catalogCaptureStderr(t, func() { PrintSummary(ColorNever, table, nil) })
	if !strings.Contains(text, "...and 6 more output files...") || strings.Contains(text, "Done in ") {
		t.Fatalf("summary did not disclose omitted outputs or optional timing: %q", text)
	}
	called := false
	text = catalogCaptureStderr(t, func() {
		PrintText(os.Stderr, LevelInfo, []string{"--log-level=warning"}, func(Colors) string { called = true; return "hidden" })
		PrintTextWithColor(os.Stderr, ColorNever, func(colors Colors) string { return colors.Green + "plain" + colors.Reset })
	})
	if called || text != "plain" {
		t.Fatalf("filtered text evaluated callback or added escapes: %v / %q", called, text)
	}
}

func TestCatalogImportAttributesRoundTripWithoutAmbiguousPacking(t *testing.T) {
	input := map[string]string{"type": "json", "empty": "", "unicode-α": "value\x00with delimiter"}
	encoded := EncodeImportAttributes(input)
	if !reflect.DeepEqual(encoded.DecodeIntoMap(), input) {
		t.Fatal("packed attributes lost arbitrary original values")
	}
	array := encoded.DecodeIntoArray()
	keys := []string{array[0].Key, array[1].Key, array[2].Key}
	if !sort.StringsAreSorted(keys) || encoded != EncodeImportAttributes(map[string]string{"unicode-α": "value\x00with delimiter", "empty": "", "type": "json"}) {
		t.Fatal("attribute key ordering changed equivalent path identity")
	}
	if EncodeImportAttributes(nil).DecodeIntoMap() != nil || EncodeImportAttributes(nil).DecodeIntoArray() != nil {
		t.Fatal("empty attributes acquired invented entries")
	}
}

func TestCatalogPlatformIndependentOutputPaths(t *testing.T) {
	for _, test := range []struct{ path, dir, base, ext string }{
		{"dir/file.module.css", "dir", "file", ".module.css"}, {`C:\file.ts`, `C:\`, "file", ".ts"},
		{`z:\`, `z:\`, "", ""}, {"/", "/", "", ""}, {"relative", "", "relative", ""},
		{"dir/file.ts///", "dir", "file", ".ts"},
	} {
		dir, base, ext := PlatformIndependentPathDirBaseExt(test.path)
		if dir != test.dir || base != test.base || ext != test.ext {
			t.Errorf("output path %q = (%q,%q,%q), want (%q,%q,%q)", test.path, dir, base, ext, test.dir, test.base, test.ext)
		}
	}
}

func TestCatalogEmbeddedStringLocationsIdentifyFollowingNativeSource(t *testing.T) {
	for _, test := range []struct{ name, outer, inner string }{
		{"plain", `'abc'`, "abc"}, {"hex", `'a\x62c'`, "abc"}, {"unicode", `'a\u0062c'`, "abc"}, {"braced unicode", `'a\u{62}c'`, "abc"},
		{"escaped newline", `'a\nc'`, "a\nc"}, {"continued line", "'a\\\r\nbc'", "abc"}, {"template line", "`a\r\nbc`", "a\nbc"},
		{"braced non-BMP", `'a\u{1F44B}c'`, "a👋c"}, {"surrogate pair", `'a\uD83D\uDC4Bc'`, "a👋c"},
		{"leading-zero BMP", `'a\u{000062}c'`, "abc"}, {"leading-zero non-BMP", `'a\u{0001F44B}c'`, "a👋c"},
		{"braced surrogate pair", `'a\u{D83D}\u{DC4B}c'`, "a👋c"}, {"fixed-braced surrogate pair", `'a\uD83D\u{DC4B}c'`, "a👋c"},
		{"braced-fixed surrogate pair", `'a\u{D83D}\uDC4Bc'`, "a👋c"}, {"leading-zero surrogate pair", `'a\u{00D83D}\u{000DC4B}c'`, "a👋c"},
		{"one-digit octal", `'a\7c'`, "a\ac"}, {"two-digit octal", `'a\62c'`, "a2c"},
		{"three-digit octal", `'a\142c'`, "abc"}, {"maximum three-digit octal", `'a\377c'`, "aÿc"},
		{"capped two-digit octal", `'a\777c'`, "a?7c"}, {"octal null", `'a\000c'`, "a\x00c"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Logf("native fixture bytes: outer=%v cooked=%v", []byte(test.outer), []byte(test.inner))
			table := GenerateStringInJSTable(test.outer, Loc{}, test.inner)
			t.Logf("native embedded-string location runs: %+v", table)
			innerLocation := Loc{Start: int32(strings.LastIndex(test.inner, "c"))}
			want := int32(strings.LastIndex(test.outer, "c"))
			if actual := RemapStringInJSLoc(table, innerLocation); actual.Start != want {
				t.Fatalf("following character remapped into an escape: %d, want original c at %d", actual.Start, want)
			}
			outer := Source{Contents: test.outer, PrettyPaths: PrettyPaths{Rel: "input.js", Abs: "/project/input.js"}}
			outerTracker := MakeLineColumnTracker(&outer)
			inner := Source{Contents: test.inner}
			innerTracker := MakeLineColumnTracker(&inner)
			base := NewDeferLog(DeferLogAll, nil)
			log := NewStringInJSLog(base, &outerTracker, table)
			log.AddErrorWithNotes(&innerTracker, Range{Loc: innerLocation, Len: 1}, "embedded error", []MsgData{innerTracker.MsgData(Range{Loc: innerLocation}, "embedded note"), {Text: "location-free note"}})
			message := log.Done()[0]
			location := message.Data.Location
			if location == nil || location.File != outer.PrettyPaths || location.LineText != outerTracker.MsgLocationOrNil(Range{Loc: Loc{Start: want}}).LineText || message.Notes[0].Location == nil || message.Notes[1].Location != nil {
				t.Fatalf("embedded diagnostic lost native source/note locations: %+v", message)
			}
		})
	}
}
