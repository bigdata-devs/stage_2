package models

// IndexBookInput is the payload the Indexing Service hands the datamart writer to index one book:
// its normalized metadata plus the token stream of its body. Tokens are kept with their
// repetitions, because the datamart derives each term's frequency from how often it appears; a
// caller therefore passes the raw stream its tokenizer produced, not a de-duplicated term set.
type IndexBookInput struct {
	Metadata BookMetadata
	Tokens   []string
}
