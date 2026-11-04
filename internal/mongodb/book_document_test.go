package mongodb

import (
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"boogle-engine/internal/models"
)

func storedKeys(t *testing.T, document any) []string {
	t.Helper()
	raw, err := bson.Marshal(document)
	if err != nil {
		t.Fatalf("marshal %T: %v", document, err)
	}
	var stored bson.D
	if err := bson.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("unmarshal %T: %v", document, err)
	}
	keys := make([]string, 0, len(stored))
	for _, element := range stored {
		keys = append(keys, element.Key)
	}
	return keys
}

func TestBookDocumentKeepsTheStoredSchema(t *testing.T) {
	metadata := models.BookMetadata{
		BookID: 1342, Title: "Pride and Prejudice", Author: "Jane Austen",
		Language: "en", PublicationYear: 1813, ReleaseYear: 1998,
	}
	document := newBookDocument(metadata, StatusPublished)

	want := []string{"_id", "title", "author", "language", "publication_year", "release_year", "status"}
	if keys := storedKeys(t, document); !slices.Equal(keys, want) {
		t.Errorf("stored keys = %q, want %q", keys, want)
	}
	if got := document.metadata(); got != metadata {
		t.Errorf("metadata round trip = %+v, want %+v", got, metadata)
	}
}
