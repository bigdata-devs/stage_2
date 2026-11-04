package mongodb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
)

const benchmarkBooks = 300

var benchmarkLanguages = [...]string{"en", "fr", "es", "de"}

const benchmarkTerm = "adventure"

func benchmarkClient(b *testing.B) Client {
	quietLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	b.Cleanup(func() { slog.SetDefault(quietLogger) })
	client := liveClient(b)
	resetDatamart(b, client)
	b.Cleanup(func() { resetDatamart(b, client) })
	if err := client.EnsureIndexes(context.Background()); err != nil {
		b.Fatalf("ensure indexes: %v", err)
	}
	return client
}

func seedBenchmarkCorpus(b *testing.B, client Client) {
	b.Helper()
	indexer := NewBookIndexer(client)
	for bookID := 1; bookID <= benchmarkBooks; bookID++ {
		if err := indexer.IndexBook(context.Background(), benchmarkInput(bookID)); err != nil {
			b.Fatalf("seed book %d: %v", bookID, err)
		}
	}
}

func benchmarkInput(bookID int) models.IndexBookInput {
	tokens := make([]string, 0, 7)
	for repeat := 0; repeat <= bookID%5; repeat++ {
		tokens = append(tokens, benchmarkTerm)
	}
	tokens = append(tokens, "voyage", fmt.Sprintf("topic%03d", bookID))
	return models.IndexBookInput{
		Metadata: models.BookMetadata{
			BookID:          models.BookID(bookID),
			Title:           fmt.Sprintf("Book %d", bookID),
			Author:          fmt.Sprintf("author %02d", bookID%20),
			Language:        benchmarkLanguages[bookID%4],
			PublicationYear: 1700 + bookID%20,
			ReleaseYear:     2000,
		},
		Tokens: tokens,
	}
}

func BenchmarkPostingsForTerms(b *testing.B) {
	client := benchmarkClient(b)
	seedBenchmarkCorpus(b, client)
	reader := NewDatamartReader(client)
	ctx := context.Background()
	terms := []string{benchmarkTerm}

	b.ResetTimer()
	for b.Loop() {
		if _, err := reader.PostingsForTerms(ctx, terms); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchBooksWithoutFilters(b *testing.B) {
	client := benchmarkClient(b)
	seedBenchmarkCorpus(b, client)
	reader := NewDatamartReader(client)
	ctx := context.Background()
	request := SearchRequest{Terms: []string{benchmarkTerm}, Limit: 20}

	b.ResetTimer()
	for b.Loop() {
		if _, err := reader.SearchBooks(ctx, request); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchBooksWithAllFilters(b *testing.B) {
	client := benchmarkClient(b)
	seedBenchmarkCorpus(b, client)
	reader := NewDatamartReader(client)
	ctx := context.Background()
	request := SearchRequest{
		Terms:    []string{benchmarkTerm},
		Author:   "author 03",
		Language: "de",
		Year:     1703,
		Limit:    20,
	}

	b.ResetTimer()
	for b.Loop() {
		if _, err := reader.SearchBooks(ctx, request); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkIndexBook(b *testing.B) {
	client := benchmarkClient(b)
	seedBenchmarkCorpus(b, client)
	indexer := NewBookIndexer(client)
	ctx := context.Background()
	inputs := make([]models.IndexBookInput, 8)
	for position := range inputs {
		inputs[position] = benchmarkInput(position + 1)
	}

	b.ResetTimer()
	iteration := 0
	for b.Loop() {
		input := inputs[iteration%len(inputs)]
		iteration++
		if err := indexer.IndexBook(ctx, input); err != nil {
			b.Fatal(err)
		}
	}
}

const realBookFixtureID models.BookID = 1342

func realBookInput(b *testing.B) models.IndexBookInput {
	b.Helper()
	directory := filepath.Join("testdata", strconv.Itoa(int(realBookFixtureID)))
	header, err := os.ReadFile(filepath.Join(directory, "header.txt"))
	if err != nil {
		b.Fatalf("read fixture header: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(directory, "body.txt"))
	if err != nil {
		b.Fatalf("read fixture body: %v", err)
	}
	tokens := indexer.ProcessText(string(body))
	if len(tokens) == 0 {
		b.Fatalf("fixture book %d produced no tokens", realBookFixtureID)
	}
	return models.IndexBookInput{
		Metadata: indexer.ExtractMetadata(realBookFixtureID, string(header)),
		Tokens:   tokens,
	}
}

func BenchmarkIndexBookRealBook(b *testing.B) {
	client := benchmarkClient(b)
	input := realBookInput(b)
	writer := NewBookIndexer(client)
	ctx := context.Background()
	if err := writer.IndexBook(ctx, input); err != nil {
		b.Fatalf("prime fixture book: %v", err)
	}
	distinctTerms := make(map[string]struct{}, len(input.Tokens))
	for _, token := range input.Tokens {
		distinctTerms[token] = struct{}{}
	}

	b.ResetTimer()
	for b.Loop() {
		if err := writer.IndexBook(ctx, input); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(distinctTerms)), "terms/book")
	b.ReportMetric(float64(len(input.Tokens)), "tokens/book")
}
