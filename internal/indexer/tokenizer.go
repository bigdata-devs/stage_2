package indexer

import "strings"

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

func Tokenize(text string) []string {
	lowered := strings.ToLower(pythonSpecialLowercase.Replace(text))
	return strings.FieldsFunc(lowered, isNotASCIILowercase)
}

func isNotASCIILowercase(character rune) bool {
	return character < 'a' || character > 'z'
}

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

func ProcessText(text string) []string {
	return Normalize(Tokenize(text))
}
