package css_ast_test

import (
	"reflect"
	"testing"

	"github.com/evanw/esbuild/internal/ast"
	css "github.com/evanw/esbuild/internal/css_ast"
	"github.com/evanw/esbuild/internal/css_lexer"
	"github.com/evanw/esbuild/internal/css_parser"
	"github.com/evanw/esbuild/internal/logger"
)

func catalogIdent(text string) css.Token {
	return css.Token{Kind: css_lexer.TIdent, Text: text}
}

func catalogDeclaration(value string) []css.Rule {
	return []css.Rule{{Data: &css.RDeclaration{KeyText: "color", Value: []css.Token{catalogIdent(value)}}}}
}

func TestCatalogRuleEqualityAndHashes(t *testing.T) {
	for _, tt := range []struct {
		name string
		make func(string) css.R
	}{
		{"charset", func(s string) css.R { return &css.RAtCharset{Encoding: s} }},
		{"keyframes", func(s string) css.R {
			return &css.RAtKeyframes{AtToken: "keyframes", Name: ast.LocRef{Ref: ast.Ref{SourceIndex: 0, InnerIndex: 0}}, Blocks: []css.KeyframeBlock{{Selectors: []string{"from"}, Rules: catalogDeclaration(s)}}}
		}},
		{"known_at", func(s string) css.R {
			return &css.RKnownAt{AtToken: "supports", Prelude: []css.Token{catalogIdent(s)}, Rules: catalogDeclaration("red")}
		}},
		{"unknown_at", func(s string) css.R {
			return &css.RUnknownAt{AtToken: "future", Prelude: []css.Token{catalogIdent(s)}, Block: []css.Token{catalogIdent("value")}}
		}},
		{"qualified", func(s string) css.R {
			return &css.RQualified{Prelude: []css.Token{catalogIdent(s)}, Rules: catalogDeclaration("red")}
		}},
		{"declaration", func(s string) css.R { return &css.RDeclaration{KeyText: "color", Value: []css.Token{catalogIdent(s)}} }},
		{"bad_declaration", func(s string) css.R { return &css.RBadDeclaration{Tokens: []css.Token{catalogIdent(s)}} }},
		{"comment", func(s string) css.R { return &css.RComment{Text: s} }},
		{"media", func(s string) css.R {
			return &css.RAtMedia{Queries: []css.MediaQuery{{Data: &css.MQType{Type: "screen"}}}, Rules: catalogDeclaration(s)}
		}},
		{"scope", func(s string) css.R { return &css.RAtScope{Rules: catalogDeclaration(s)} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, b, changed := tt.make("red"), tt.make("red"), tt.make("blue")
			if !a.Equal(b, nil) || !b.Equal(a, nil) || a.Equal(changed, nil) {
				t.Fatalf("equality failed identical/changed rule behavior: %T", a)
			}
			ha, oka := a.Hash()
			hb, okb := b.Hash()
			if !oka || !okb || ha != hb {
				t.Fatalf("equal %T rules have different/disabled hashes: %x/%x", a, ha, hb)
			}
			if a.Equal(&css.RAtImport{}, nil) {
				t.Fatalf("%T matched an import rule", a)
			}
		})
	}
	importRule := &css.RAtImport{ImportRecordIndex: 0}
	remover := css_parser.MakeDeadRuleMangler(ast.SymbolMap{})
	imports := []css.Rule{{Data: importRule}, {Data: &css.RAtImport{ImportRecordIndex: 0}}}
	if got := remover.RemoveDeadRulesInPlace(0, imports, []ast.ImportRecord{{Path: logger.Path{Text: "theme.css"}}}); len(got) != 2 {
		t.Fatal("import declarations were deduplicated despite per-occurrence side effects")
	}
}

func TestCatalogLayersPreserveFirstDeclarationOrder(t *testing.T) {
	// CSS Cascade 5 §6.4.3 orders named layers by their first declaration.
	// Each anonymous layer is also distinct (§6.4.2.1). Keeping the last copy,
	// as ordinary dead-rule removal does, would change these semantics.
	for _, tt := range []struct {
		name  string
		names [][]string
	}{
		{"named", [][]string{{"base"}}}, {"nested", [][]string{{"base", "theme"}}}, {"anonymous", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first := &css.RAtLayer{Names: tt.names, Rules: catalogDeclaration("red")}
			middle := &css.RAtLayer{Names: [][]string{{"other"}}, Rules: catalogDeclaration("blue")}
			last := &css.RAtLayer{Names: tt.names, Rules: catalogDeclaration("red")}
			remover := css_parser.MakeDeadRuleMangler(ast.SymbolMap{})
			rules := []css.Rule{{Data: first}, {Data: middle}, {Data: last}}
			got := remover.RemoveDeadRulesInPlace(0, rules, nil)
			if len(got) != 3 || got[0].Data != first || got[1].Data != middle || got[2].Data != last {
				t.Fatalf("layer order/occurrences changed: %+v", got)
			}
		})
	}
}

func TestCatalogSelectorCloneOwnsNestedArguments(t *testing.T) {
	children := []css.Token{catalogIdent("original")}
	args := []css.Token{{Kind: css_lexer.TFunction, Text: "nested", Children: &children}}
	pseudo := &css.SSPseudoClass{Name: "lang", Args: args}
	selector := css.ComplexSelector{Selectors: []css.CompoundSelector{{SubclassSelectors: []css.SubclassSelector{{Data: pseudo}}}}}
	clone := selector.Clone()
	if &pseudo.Args[0] != &args[0] || pseudo.Args[0].Children != &children {
		t.Error("cloning changed the original selector's argument ownership")
	}
	clonedPseudo, ok := clone.Selectors[0].SubclassSelectors[0].Data.(*css.SSPseudoClass)
	if !ok {
		t.Fatal("clone changed pseudo-class kind")
	}
	(*clonedPseudo.Args[0].Children)[0].Text = "changed"
	clonedPseudo.Args[0].Text = "replacement"
	if children[0].Text != "original" || args[0].Text != "nested" {
		t.Fatalf("clone mutation reached caller-owned tokens: args=%+v children=%+v", args, children)
	}
	if pseudo.Args[0].Text != "nested" || (*pseudo.Args[0].Children)[0].Text != "original" {
		t.Fatal("clone mutation changed original selector")
	}
}

func TestCatalogTokenCloneRebindsNestedURLs(t *testing.T) {
	child := []css.Token{{Kind: css_lexer.TURL, Text: "url", PayloadIndex: 1, Loc: logger.Loc{Start: 19}}}
	tokens := []css.Token{{Kind: css_lexer.TFunction, Text: "image-set", Children: &child, Loc: logger.Loc{Start: 4}}, {Kind: css_lexer.TURL, Text: "url", PayloadIndex: 0, Loc: logger.Loc{Start: 29}}}
	imports := []ast.ImportRecord{{Path: logger.Path{Text: "first.svg"}}, {Path: logger.Path{Text: "second.svg"}}}
	existing := []ast.ImportRecord{{Path: logger.Path{Text: "existing.svg"}}}
	clone, records := css.CloneTokensWithImportRecords(tokens, imports, nil, existing)
	if len(records) != 3 || records[1].Path.Text != "second.svg" || records[2].Path.Text != "first.svg" || (*clone[0].Children)[0].PayloadIndex != 1 || clone[1].PayloadIndex != 2 {
		t.Fatalf("nested URL rebinding = %+v / %+v", clone, records)
	}
	if clone[0].Loc.Start != 0 || (*clone[0].Children)[0].Loc.Start != 0 || clone[1].Loc.Start != 0 || tokens[0].Loc.Start != 4 || child[0].Loc.Start != 19 {
		t.Fatal("cross-file clone did not reset clone locations while retaining input locations")
	}
	(*clone[0].Children)[0].Text = "changed"
	if child[0].Text != "url" {
		t.Fatal("nested URL clone aliases input")
	}
	check := &css.CrossFileEqualityCheck{ImportRecordsA: imports, ImportRecordsB: records}
	if !tokens[1].Equal(clone[1], check) {
		t.Fatal("same URL import differs after index rebinding")
	}
	records[2].Path.Text = "different.svg"
	if tokens[1].Equal(clone[1], check) {
		t.Fatal("different resolved URL imports compare equal")
	}
}

func TestCatalogMediaQueryEqualityAndClone(t *testing.T) {
	for _, tt := range []struct {
		name string
		make func(css.WhitespaceFlags) css.MQ
	}{
		{"type", func(w css.WhitespaceFlags) css.MQ {
			return &css.MQType{Type: "screen", AndOrNull: css.MediaQuery{Data: &css.MQPlainOrBoolean{Name: "width", ValueOrNil: []css.Token{{Kind: css_lexer.TDimension, Text: "10px", UnitOffset: 2, Whitespace: w}}}}}
		}},
		{"not", func(w css.WhitespaceFlags) css.MQ {
			return &css.MQNot{Inner: css.MediaQuery{Data: &css.MQArbitraryTokens{Tokens: []css.Token{{Kind: css_lexer.TIdent, Text: "color", Whitespace: w}}}}}
		}},
		{"binary", func(w css.WhitespaceFlags) css.MQ {
			return &css.MQBinary{Op: css.MQBinaryOpAnd, Terms: []css.MediaQuery{{Data: &css.MQArbitraryTokens{Tokens: []css.Token{{Kind: css_lexer.TIdent, Text: "color", Whitespace: w}}}}}}
		}},
		{"arbitrary", func(w css.WhitespaceFlags) css.MQ {
			return &css.MQArbitraryTokens{Tokens: []css.Token{{Kind: css_lexer.TIdent, Text: "future", Whitespace: w}}}
		}},
		{"plain", func(w css.WhitespaceFlags) css.MQ {
			return &css.MQPlainOrBoolean{Name: "width", ValueOrNil: []css.Token{{Kind: css_lexer.TDimension, Text: "10px", UnitOffset: 2, Whitespace: w}}}
		}},
		{"range", func(w css.WhitespaceFlags) css.MQ {
			return &css.MQRange{Name: "width", BeforeCmp: css.MQCmpLt, Before: []css.Token{{Kind: css_lexer.TDimension, Text: "10px", UnitOffset: 2, Whitespace: w}}, AfterCmp: css.MQCmpLe, After: []css.Token{catalogIdent("20px")}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, b := tt.make(0), tt.make(css.WhitespaceBefore)
			if a.Equal(b, nil) || !a.EqualIgnoringWhitespace(b) {
				t.Fatal("query equality failed whitespace distinction")
			}
			clone, records := a.CloneWithImportRecords(nil, nil)
			if !a.Equal(clone, nil) || a.Hash() != clone.Hash() || len(records) != 0 || !reflect.DeepEqual(a, clone) {
				t.Fatalf("query clone changed semantics: %#v / %#v", a, clone)
			}
			if a.Equal(&css.MQPlainOrBoolean{Name: "different"}, nil) || a.EqualIgnoringWhitespace(&css.MQPlainOrBoolean{Name: "different"}) {
				t.Fatal("different query shape/name matched")
			}
		})
	}
	url := []css.Token{{Kind: css_lexer.TURL, PayloadIndex: 0}}
	input := []css.MediaQuery{{Data: &css.MQNot{Inner: css.MediaQuery{Data: &css.MQArbitraryTokens{Tokens: url}}}}}
	clones, records := css.CloneMediaQueriesWithImportRecords(input, []ast.ImportRecord{{Path: logger.Path{Text: "query.svg"}}}, nil, nil)
	if len(records) != 1 || records[0].Path.Text != "query.svg" || !css.MediaQueriesEqual(input, clones, nil) || !css.MediaQueriesEqualIgnoringWhitespace(input, clones) {
		t.Fatal("media clone failed nested URL inventory/semantics")
	}
	cloned := clones[0].Data.(*css.MQNot).Inner.Data.(*css.MQArbitraryTokens)
	cloned.Tokens[0].Text = "changed"
	if url[0].Text != "" {
		t.Fatal("media clone aliases caller tokens")
	}
}
