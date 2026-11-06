package mongodb

import "go.mongodb.org/mongo-driver/v2/mongo"

type DatamartReader struct {
	client Client
}

func NewDatamartReader(client Client) DatamartReader {
	return DatamartReader{client: client}
}

func (reader DatamartReader) books() *mongo.Collection {
	return reader.client.database.Collection(BooksCollection)
}

func (reader DatamartReader) index() *mongo.Collection {
	return reader.client.database.Collection(InvertedIndexCollection)
}
