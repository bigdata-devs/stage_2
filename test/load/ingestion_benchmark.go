// Command ingestion_benchmark drives the Ingestion Service through HTTP and reports
// throughput in books/s. Only 2xx answers count as ingested; every other status is
// reported as a failure and excluded from the throughput numerator.
//
// Usage:
//
//	go run ./test/load/ingestion_benchmark.go
//	go run ./test/load/ingestion_benchmark.go -url http://localhost:7001 -concurrency 10
//	go run ./test/load/ingestion_benchmark.go -books 11,26,43 -out results.txt
//
// The runner test/load/ingestion_throughput.ps1 builds this file against a cold
// datalake at several -workers settings and aggregates the outputs.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// defaultBooks mirrors the small corpus used by the original throughput script;
// the runner replaces it with the ~100-book list of the official benchmark.
const defaultBooks = "1342,84,11,174,2701,1661,1232,98,4300,2500,55,76,120,408,844,190,205,345,500,900,111,222,333,444,555,666,777,888,999,1010"

func main() {
	url := flag.String("url", "http://localhost:7001", "base URL of the Ingestion Service")
	books := flag.String("books", defaultBooks, "comma-separated Gutenberg book IDs")
	concurrency := flag.Int("concurrency", 10, "max concurrent POST /ingest requests")
	timeout := flag.Duration("timeout", 120*time.Second, "per-request HTTP timeout")
	out := flag.String("out", "ingestion_throughput_results.txt", "result transcript path")
	flag.Parse()

	bookIDs := parseBooks(*books)
	sem := make(chan struct{}, max(1, *concurrency))
	var wg sync.WaitGroup
	var ingested, failed int64
	var mu sync.Mutex
	statusCounts := map[int]int{}

	fmt.Println("=== INGESTION SERVICE THROUGHPUT BENCHMARK ===")
	fmt.Printf("URL:          %s\n", *url)
	fmt.Printf("Books:        %d\n", len(bookIDs))
	fmt.Printf("Concurrency:  %d\n", *concurrency)
	fmt.Printf("Timeout:      %s\n", *timeout)
	fmt.Println("Counting only HTTP 2xx answers as ingested.")

	client := &http.Client{Timeout: *timeout}
	start := time.Now()

	for _, id := range bookIDs {
		wg.Add(1)
		go func(bookID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			status := postBook(client, *url, bookID)
			mu.Lock()
			statusCounts[status]++
			mu.Unlock()
			if status >= 200 && status < 300 {
				atomic.AddInt64(&ingested, 1)
			} else {
				atomic.AddInt64(&failed, 1)
			}
		}(id)
	}

	wg.Wait()
	elapsed := time.Since(start)
	throughput := float64(atomic.LoadInt64(&ingested)) / elapsed.Seconds()

	var b strings.Builder
	fmt.Fprintf(&b, "-----------------------------------\n")
	fmt.Fprintf(&b, "Books attempted:     %d\n", len(bookIDs))
	fmt.Fprintf(&b, "Ingested (2xx):      %d\n", ingested)
	fmt.Fprintf(&b, "Failed (non-2xx):    %d\n", failed)
	fmt.Fprintf(&b, "Total elapsed time:  %.2f seconds\n", elapsed.Seconds())
	fmt.Fprintf(&b, "Ingestion throughput: %.2f books/second (2xx only)\n", throughput)
	fmt.Fprintf(&b, "Status breakdown:    %s\n", formatStatusCounts(statusCounts))
	fmt.Fprintf(&b, "-----------------------------------\n")

	fmt.Print(b.String())
	if err := os.WriteFile(*out, []byte(b.String()), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save results: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Results saved to %s\n", *out)
}

// postBook sends one POST /ingest/{id} and returns the HTTP status code
// (0 when the request failed at transport level).
func postBook(client *http.Client, baseURL, bookID string) int {
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/ingest/"+bookID, http.NoBody)
	if err != nil {
		return 0
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode
}

func parseBooks(raw string) []string {
	parts := strings.Split(raw, ",")
	ids := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			ids = append(ids, p)
		}
	}
	return ids
}

func formatStatusCounts(counts map[int]int) string {
	if len(counts) == 0 {
		return "(none)"
	}
	keys := make([]int, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d:%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}
