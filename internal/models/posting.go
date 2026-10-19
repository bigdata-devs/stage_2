package models

// Posting is one entry of a term's posting list: a book that contains the term, together with
// how many times the term appears in that body (its term frequency, tf). The pair is the unit
// the inverted index stores per term and the unit the Search Service scores when ranking. The
// bson tags match the wire names the index pipelines and the Search Service rely on.
type Posting struct {
	BookID BookID `json:"book_id" bson:"book_id"`
	TF     int    `json:"tf" bson:"tf"`
}

// TermPostings associates one normalized term with every book that contains it. Postings are
// kept sorted by book ID and free of duplicates, so two runs over the same corpus produce the
// same document and a repeated indexing of a book is idempotent.
type TermPostings struct {
	Term     string
	Postings []Posting
}
