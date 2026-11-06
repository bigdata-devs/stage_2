package indexer_test

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"boogle-engine/internal/crawler"
	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
)

type benchmarkBook struct {
	ID     models.BookID
	Header string
	Body   string
}

func benchmarkBooks(b *testing.B) []benchmarkBook {
	b.Helper()
	root := os.Getenv("BOOGLE_BENCH_DATALAKE")
	if root == "" {
		root = filepath.Join("..", "..", "datalake")
	}
	bodyPaths, err := filepath.Glob(filepath.Join(root, "batch_*", "*", "*_body.txt"))
	if err != nil {
		b.Fatalf("find the datalake books: %v", err)
	}
	if len(bodyPaths) == 0 {
		b.Skipf("no books under %s; set BOOGLE_BENCH_DATALAKE to the datalake folder", root)
	}
	sort.Strings(bodyPaths)
	books := make([]benchmarkBook, 0, len(bodyPaths))
	for _, bodyPath := range bodyPaths {
		book, err := loadBenchmarkBook(bodyPath)
		if err != nil {
			b.Fatalf("load %s: %v", bodyPath, err)
		}
		books = append(books, book)
	}
	return books
}

func loadBenchmarkBook(bodyPath string) (benchmarkBook, error) {
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		return benchmarkBook{}, err
	}
	header, err := os.ReadFile(strings.TrimSuffix(bodyPath, "_body.txt") + "_header.txt")
	if err != nil {
		return benchmarkBook{}, err
	}
	id, err := strconv.ParseUint(filepath.Base(filepath.Dir(bodyPath)), 10, 64)
	if err != nil {
		return benchmarkBook{}, err
	}
	return benchmarkBook{ID: models.BookID(id), Header: string(header), Body: string(body)}, nil
}

func cycle(b *testing.B, count int, operation func(position int)) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(probes *testing.PB) {
		position := 0
		for probes.Next() {
			operation(position % count)
			position++
		}
	})
}

func averageSize(sizes []int) int64 {
	total := 0
	for _, size := range sizes {
		total += size
	}
	return int64(total / len(sizes))
}

func BenchmarkProcessText(b *testing.B) {
	books := benchmarkBooks(b)
	bodySizes := make([]int, len(books))
	for position, book := range books {
		bodySizes[position] = len(book.Body)
	}
	b.SetBytes(averageSize(bodySizes))
	cycle(b, len(books), func(position int) {
		indexer.ProcessText(books[position].Body)
	})
}

func BenchmarkExtractMetadata(b *testing.B) {
	books := benchmarkBooks(b)
	headerSizes := make([]int, len(books))
	for position, book := range books {
		headerSizes[position] = len(book.Header)
	}
	b.SetBytes(averageSize(headerSizes))
	cycle(b, len(books), func(position int) {
		book := books[position]
		indexer.ExtractMetadata(book.ID, book.Header)
	})
}

func BenchmarkSplitBookText(b *testing.B) {
	books := benchmarkBooks(b)
	rawTexts := make([]string, len(books))
	rawSizes := make([]int, len(books))
	for position, book := range books {
		rawTexts[position] = gutenbergText(book)
		rawSizes[position] = len(rawTexts[position])
		if _, err := crawler.SplitBookText(book.ID, rawTexts[position]); err != nil {
			b.Fatalf("split the reconstructed book %d: %v", book.ID, err)
		}
	}
	b.SetBytes(averageSize(rawSizes))
	cycle(b, len(books), func(position int) {
		_, _ = crawler.SplitBookText(books[position].ID, rawTexts[position])
	})
}

func gutenbergText(book benchmarkBook) string {
	const licenceFooter = "\n\nProduced by an anonymous volunteer.\n"
	return book.Header + "\n\n" +
		crawler.StartMarker + " ***\n\n" +
		book.Body + "\n\n" +
		crawler.EndMarker + " ***\n" + licenceFooter
}
