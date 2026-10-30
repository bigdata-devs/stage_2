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

const defaultBooks = "10,11,16,23,35,41,42,43,45,46,55,74,76,84,98,100,120,145,161,174,204,205,209,244,345,394,408,768,844,863,1080,1184,1232,1259,1260,1342,1400,1497,1513,1661,1695,1727,1952,1998,2148,2160,2465,2542,2554,2591,2600,2641,2680,2701,2852,3207,3268,3296,4085,4300,4363,5197,5200,6593,6761,7370,8492,12342,15399,16328,16389,20203,25344,26471,28054,34901,37106,37683,49008,49297,57669,58820,58866,59828,59892,61168,61262,61851,62215,62383,63469,64317,65238,67979,68283,69087,78004,78005,78009,78010"

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
