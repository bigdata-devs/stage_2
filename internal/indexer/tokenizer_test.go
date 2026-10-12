package indexer

import (
	"slices"
	"testing"
)

// The cases mirror tests/test_utils/test_text_processor.py and the Stage 1 Go port.

func TestTokenize(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		expected []string
	}{
		{"lowercases", "HELLO World", []string{"hello", "world"}},
		{"removes punctuation", "hello, world!", []string{"hello", "world"}},
		{"splits hyphens", "well-known", []string{"well", "known"}},
		{"empty string", "", []string{}},
		{"keeps letters of alphanumerics", "word123 another", []string{"word", "another"}},
		{"splits on non-ASCII letters", "caf\u00e9 na\u00efve", []string{"caf", "na", "ve"}},
		{"Python lowercase of dotted capital I", "\u0130stanbul", []string{"i", "stanbul"}},
		{"Kelvin sign lowercases to k", "\u212Aey", []string{"key"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if tokens := Tokenize(testCase.text); !slices.Equal(tokens, testCase.expected) {
				t.Fatalf("expected %q, got %q", testCase.expected, tokens)
			}
		})
	}
}

func TestNormalizeDropsStopWordsRomanNumeralsAndSingleLetters(t *testing.T) {
	tokens := Normalize([]string{"the", "cat", "ii", "a", "b", "one", "hundred", "adventure"})
	if expected := []string{"cat", "hundred", "adventure"}; !slices.Equal(tokens, expected) {
		t.Fatalf("expected %q, got %q", expected, tokens)
	}
}

func TestProcessTextRunsFullPipeline(t *testing.T) {
	tokens := ProcessText("The Cat is on the Mat, Chapter II!")
	if expected := []string{"cat", "mat", "chapter"}; !slices.Equal(tokens, expected) {
		t.Fatalf("expected %q, got %q", expected, tokens)
	}
}
