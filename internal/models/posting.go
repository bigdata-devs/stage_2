package models

type Posting struct {
	BookID BookID
	TF     int
}

type TermPostings struct {
	Term     string
	Postings []Posting
}
