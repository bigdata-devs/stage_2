package models

type Posting struct {
	BookID BookID `json:"book_id" bson:"book_id"`
	TF     int    `json:"tf" bson:"tf"`
}

type TermPostings struct {
	Term     string
	Postings []Posting
}
