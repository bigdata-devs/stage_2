package models

type BookMetadata struct {
	BookID          BookID
	Title           string
	Author          string
	Language        string
	PublicationYear int
	ReleaseYear     int
}
