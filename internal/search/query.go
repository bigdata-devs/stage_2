package search

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"boogle-engine/internal/indexer"
)

const (
	DefaultLimit  = 20
	MaxLimit      = 100
	MaxQueryTerms = 10
	MinYear       = 1
	MaxYear       = 9999
	MaxOffset     = math.MaxInt32
)

type Field string

const (
	FieldText   Field = "text"
	FieldYear   Field = "year"
	FieldOffset Field = "offset"
	FieldLimit  Field = "limit"
)

var ErrInvalidQuery = errors.New("invalid search query")

type InvalidQueryError struct {
	Field   Field
	Missing bool
	Rule    string
}

func (err InvalidQueryError) Error() string {
	if err.Missing {
		return fmt.Sprintf("%s is required", err.Field)
	}
	return fmt.Sprintf("%s %s", err.Field, err.Rule)
}

func (err InvalidQueryError) Unwrap() error {
	return ErrInvalidQuery
}

type numberRule struct {
	minimum int
	maximum int
	rule    string
}

var numberRules = map[Field]numberRule{
	FieldYear:   {minimum: MinYear, maximum: MaxYear, rule: fmt.Sprintf("must be a year between %d and %d", MinYear, MaxYear)},
	FieldOffset: {minimum: 0, maximum: MaxOffset, rule: "must be a non-negative integer"},
	FieldLimit:  {minimum: 1, maximum: MaxLimit, rule: fmt.Sprintf("must be an integer between 1 and %d", MaxLimit)},
}

func InvalidNumber(field Field) InvalidQueryError {
	return InvalidQueryError{Field: field, Rule: numberRules[field].rule}
}

func newQuery(request Request) (Query, error) {
	text := strings.TrimSpace(request.Text)
	if text == "" {
		return Query{}, InvalidQueryError{Field: FieldText, Missing: true}
	}
	terms, err := parseTerms(text)
	if err != nil {
		return Query{}, err
	}
	year, err := numberOrDefault(FieldYear, request.Year, 0)
	if err != nil {
		return Query{}, err
	}
	offset, err := numberOrDefault(FieldOffset, request.Offset, 0)
	if err != nil {
		return Query{}, err
	}
	limit, err := numberOrDefault(FieldLimit, request.Limit, DefaultLimit)
	if err != nil {
		return Query{}, err
	}
	return Query{
		Text:  text,
		Terms: terms,
		Filters: Filters{
			Author:   strings.TrimSpace(request.Author),
			Language: strings.ToLower(strings.TrimSpace(request.Language)),
			Year:     year,
		},
		Offset: offset,
		Limit:  limit,
	}, nil
}

func parseTerms(text string) ([]string, error) {
	words := indexer.Tokenize(text)
	if len(words) == 0 {
		return nil, InvalidQueryError{Field: FieldText, Rule: "must contain at least one word made of the letters a-z"}
	}
	terms := []string{}
	seen := map[string]bool{}
	for _, term := range indexer.Normalize(words) {
		if seen[term] {
			continue
		}
		if len(terms) == MaxQueryTerms {
			return nil, InvalidQueryError{Field: FieldText, Rule: fmt.Sprintf("must contain at most %d distinct search words", MaxQueryTerms)}
		}
		seen[term] = true
		terms = append(terms, term)
	}
	return terms, nil
}

func numberOrDefault(field Field, value *int, fallback int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	rule := numberRules[field]
	if *value < rule.minimum || *value > rule.maximum {
		return 0, InvalidNumber(field)
	}
	return *value, nil
}
