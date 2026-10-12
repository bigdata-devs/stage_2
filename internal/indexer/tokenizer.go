package indexer

import "strings"

// romanNumerals and stopWords mirror the Stage 1 tokenizer (src/utils/text_processor.py and
// multilang_ports/go/utils/text_processor.go) so every stage indexes the same token stream.
var romanNumerals = newWordSet(
	"i", "ii", "iii", "iv", "v", "vi", "vii", "viii", "ix", "x",
	"xi", "xii", "xiii", "xiv", "xv", "xvi", "xvii", "xviii", "xix", "xx",
	"xxi", "xxii", "xxiii", "xxiv", "xxv", "xxvi", "xxvii", "xxviii", "xxix", "xxx",
	"xl", "l", "lx", "lxx", "lxxx", "xc", "c",
)

var stopWords = newWordSet(
	"a", "an", "the", "and", "or", "but", "in", "on", "at", "to", "for",
	"of", "with", "by", "is", "are", "was", "were", "be", "been", "being",
	"have", "has", "had", "do", "does", "did", "will", "would", "could",
	"should", "may", "might", "shall", "can", "it", "its", "he", "she",
	"they", "them", "his", "her", "their", "this", "that", "these", "those",
	"i", "you", "we", "me", "my", "your", "our", "not", "no", "so",
	"if", "then", "than", "too", "very", "just", "about", "also",
	"as", "from", "into", "up", "out", "off", "over", "under", "again",
	"there", "here", "when", "where", "why", "how", "all", "each", "every",
	"both", "few", "more", "most", "other", "some", "such", "only", "own",
	"same", "now", "new", "one", "two", "three", "many", "much",
	"well", "back", "even", "still", "before", "after", "between", "through",
)

// pythonSpecialLowercase reproduces the one unconditional multi-rune mapping of Python's
// str.lower() that strings.ToLower skips: U+0130 (capital I with dot) becomes "i" plus U+0307 (combining dot).
var pythonSpecialLowercase = strings.NewReplacer("\u0130", "i\u0307")

type wordSet map[string]struct{}

func newWordSet(words ...string) wordSet {
	set := make(wordSet, len(words))
	for _, word := range words {
		set[word] = struct{}{}
	}
	return set
}

func (set wordSet) contains(word string) bool {
	_, found := set[word]
	return found
}

// Tokenize lowercases the text and returns every maximal run of ASCII letters, exactly like
// re.findall(r"[a-z]+", text.lower()) in Stage 1.
func Tokenize(text string) []string {
	lowered := strings.ToLower(pythonSpecialLowercase.Replace(text))
	return strings.FieldsFunc(lowered, isNotASCIILowercase)
}

// isNotASCIILowercase splits on every rune outside a-z; multi-byte UTF-8 runes always split,
// as they do in the Python regular expression.
func isNotASCIILowercase(character rune) bool {
	return character < 'a' || character > 'z'
}

// Normalize drops stop words, Roman numerals and single-letter tokens.
func Normalize(tokens []string) []string {
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if isIndexable(token) {
			kept = append(kept, token)
		}
	}
	return kept
}

func isIndexable(token string) bool {
	return len(token) > 1 && !stopWords.contains(token) && !romanNumerals.contains(token)
}

// ProcessText runs the full text pipeline: Tokenize followed by Normalize.
func ProcessText(text string) []string {
	return Normalize(Tokenize(text))
}
