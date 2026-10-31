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

// The benchmarks below exercise a live server and skip when none is reachable (the same policy
// as the tests). Each measures a whole second or more of operations, which is also what keeps
// them honest on platforms whose monotonic clock reads sub-millisecond spans as zero: the
// framework averages over the full window instead of trusting one tick.

// benchmarkBooks is the corpus size: big enough that a common term carries a three-digit posting
// list and the filter indexes have real work to do, small enough to reseed between runs.
const benchmarkBooks = 300

// benchmarkLanguages cycles over the corpus so language filters keep their selectivity.
var benchmarkLanguages = [...]string{"en", "fr", "es", "de"}

// benchmarkTerm is contained in every corpus book, so every read benchmark joins the full
// posting list — the expensive path a reader takes for a common word.
const benchmarkTerm = "adventure"

// benchmarkClient prepares an empty datamart with the production indexes created, and empties it
// again when the benchmark finishes, so consecutive runs start from the same state. It silences
// the logger while seeding: a stray setup line between a benchmark's name and its result would
// keep benchstat from parsing the output.
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

// seedBenchmarkCorpus indexes benchmarkBooks books through the real write path, so the reads
// measure against the data shape production actually produces.
func seedBenchmarkCorpus(b *testing.B, client Client) {
	b.Helper()
	indexer := NewBookIndexer(client)
	for bookID := 1; bookID <= benchmarkBooks; bookID++ {
		if err := indexer.IndexBook(context.Background(), benchmarkInput(bookID)); err != nil {
			b.Fatalf("seed book %d: %v", bookID, err)
		}
	}
}

// benchmarkInput builds one corpus book. Author, language and year are aligned on the book ID
// (bookID%20 fixes all three at once, because 20 is a multiple of 4), so the combined filter
// author "author 03" + language "de" + year 1703 selects exactly the 15 books of that group.
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

// BenchmarkPostingsForTerms retrieves the raw posting list of the common term: the baseline the
// aggregation on top of it, and the query the granular reader serves for scoring pipelines.
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

// BenchmarkSearchBooksWithoutFilters runs the full search aggregation over the common term:
// join with book metadata, published gate, total count and one relevance-ordered page, all in
// one server round trip. This is the shape of GET /search without query parameters.
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

// BenchmarkSearchBooksWithAllFilters runs the same aggregation with author, language and year
// filters attached, which each posting's book must satisfy in the sub-pipeline. Comparing it
// against the filterless run prices what the metadata filters cost on top of the join.
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

// BenchmarkIndexBook re-indexes eight existing corpus books in rotation through the real write
// path (pending marker, posting upserts, publish). Steady-state updating of stored books is the
// hot path of POST /index/update; the tokenizer is outside this package and not timed here.
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

// realBookFixtureID is the datalake book the real-book benchmark indexes: Pride and Prejudice.
// Its tokenized body holds 6,576 distinct terms — the low end of the 6.5k–13k range a typical
// novel produces — and it is the book every demo, k6 script and handler test already uses.
const realBookFixtureID models.BookID = 1342

// realBookInput loads the fixture book and runs it through the production pipeline: metadata
// parsed from the Gutenberg header, tokens from the tokenizer. The benchmarks exclude this step,
// exactly as the write path receives an already parsed book.
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

// BenchmarkIndexBookRealBook re-indexes a real novel on every iteration, so in steady state each
// call rewrites one posting array per distinct term twice (drop the previous version, upsert the
// current one). The terms/book and tokens/book metrics put numbers behind MongoDB's CPU load
// during indexing: they are the posting arrays each IndexBook call touches.
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
