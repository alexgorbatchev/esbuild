package ast

import (
	"testing"

	"github.com/evanw/esbuild/internal/helpers"
)

func TestCatalogSymbolMergePreservesIdentity(t *testing.T) {
	symbols := NewSymbolMap(2)
	symbols.SymbolsForSource[0] = []Symbol{
		{OriginalName: "Widget", Kind: SymbolOther, Link: InvalidRef, UseCountEstimate: 2, Flags: MustNotBeRenamed | MustStartWithCapitalLetterForJSX},
		{OriginalName: "middle", Kind: SymbolOther, Link: InvalidRef, UseCountEstimate: 3},
	}
	symbols.SymbolsForSource[1] = []Symbol{{OriginalName: "final", Kind: SymbolOther, Link: InvalidRef, UseCountEstimate: 5}}
	first, middle, final := Ref{0, 0}, Ref{0, 1}, Ref{1, 0}
	MergeSymbols(symbols, first, middle)
	MergeSymbols(symbols, first, final)
	if got := MergeSymbols(symbols, middle, first); got != final {
		t.Fatalf("linked merge returned %v, want final %v", got, final)
	}
	FollowAllSymbols(symbols)
	for _, ref := range []Ref{first, middle, final} {
		if got := FollowSymbols(symbols, ref); got != final {
			t.Fatalf("reference %v follows to %v, want %v", ref, got, final)
		}
	}
	s := symbols.Get(final)
	if s.UseCountEstimate != 10 || s.OriginalName != "Widget" || s.SlotNamespace() != SlotMustNotBeRenamed || !s.Flags.Has(MustStartWithCapitalLetterForJSX) {
		t.Fatalf("merged symbol lost use count, name or JSX constraints: %+v", s)
	}
	if MergeSymbols(symbols, final, final) != final || s.UseCountEstimate != 10 {
		t.Fatal("merging a symbol with itself changed its identity or uses")
	}
}

func TestCatalogSymbolSlots(t *testing.T) {
	tests := []struct {
		name  string
		kind  SymbolKind
		flags SymbolFlags
		want  SlotNamespace
	}{
		{"ordinary", SymbolOther, 0, SlotDefault},
		{"private", SymbolPrivateField, 0, SlotPrivateName},
		{"label", SymbolLabel, 0, SlotLabel},
		{"property", SymbolMangledProp, 0, SlotMangledProp},
		{"unbound", SymbolUnbound, 0, SlotMustNotBeRenamed},
		{"fixed_private", SymbolPrivateField, MustNotBeRenamed, SlotMustNotBeRenamed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Symbol{Kind: tt.kind, Flags: tt.flags}
			if got := s.SlotNamespace(); got != tt.want {
				t.Fatalf("slot = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCatalogImportAttributeLookup(t *testing.T) {
	entries := []AssertOrWithEntry{
		{Key: helpers.StringToUTF16("type"), Value: helpers.StringToUTF16("json")},
		{Key: helpers.StringToUTF16("é🦊"), Value: helpers.StringToUTF16("unicode")},
	}
	for _, name := range []string{"type", "é🦊"} {
		entry := FindAssertOrWithEntry(entries, name)
		if entry == nil || len(entry.Value) == 0 || !helpers.UTF16EqualsString(entry.Key, name) {
			t.Fatalf("attribute lookup failed for %q: %+v", name, entry)
		}
	}
	if FindAssertOrWithEntry(entries, "Type") != nil || FindAssertOrWithEntry(nil, "type") != nil {
		t.Fatal("lookup matched absent or differently cased attribute")
	}
}

func TestCatalogFrequencyChangesMinifiedNames(t *testing.T) {
	var freq, other CharFreq
	freq.Scan("ZZZ123___$$", 1)
	freq.Scan("___", -1)
	freq.Scan("abc", 0)
	other.Scan("Z", 1)
	freq.Include(&other)
	minifier := DefaultNameMinifierJS.ShuffleByCharFreq(freq)
	if got := minifier.NumberToMinifiedName(0); got != "Z" {
		t.Fatalf("most frequent identifier head = %q, want Z", got)
	}
	if got := minifier.NumberToMinifiedName(1); got != "$" {
		t.Fatalf("second identifier head = %q, want $", got)
	}
	if DefaultNameMinifierJS.NumberToMinifiedName(54) != "aa" {
		t.Fatal("identifier sequence did not advance to two characters")
	}
}
