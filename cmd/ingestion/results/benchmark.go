package main

import (
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

func main() {
	bookIDs := []int{
		1342, 84, 11, 174, 2701, 1661, 1232, 98, 4300, 2500,
		55, 76, 120, 408, 844, 190, 205, 345, 500, 900,
		111, 222, 333, 444, 555, 666, 777, 888, 999, 1010,
	}
	concurrentRequests := 10 // Simulating 10 concurrent workers/users

	fmt.Println("=== INGESTION SERVICE BENCHMARK ===")
	fmt.Printf("Testing with %d books and %d concurrent requests...\n", len(bookIDs), concurrentRequests)

	startTime := time.Now()

	sem := make(chan struct{}, concurrentRequests)
	var wg sync.WaitGroup

	for _, id := range bookIDs {
		wg.Add(1)
		go func(bookID int) {
			defer wg.Done()
			sem <- struct{}{}

			url := fmt.Sprintf("http://localhost:7001/ingest/%d", bookID)
			req, _ := http.NewRequest("POST", url, nil)
			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Do(req)

			if err == nil {
				resp.Body.Close()
			}

			<-sem
		}(id)
	}

	wg.Wait()

	elapsed := time.Since(startTime).Seconds()
	throughput := float64(len(bookIDs)) / elapsed

	resultText := "-----------------------------------\n"
	resultText += fmt.Sprintf("Total books processed: %d\n", len(bookIDs))
	resultText += fmt.Sprintf("Total elapsed time: %.2f seconds\n", elapsed)
	resultText += fmt.Sprintf("Ingestion Throughput: %.2f books/second\n", throughput)
	resultText += "-----------------------------------\n"

	fmt.Print(resultText)

	err := os.WriteFile("ingestion_throughput_results.txt", []byte(resultText), 0644)
	if err != nil {
		fmt.Printf("Failed to save results: %v\n", err)
	} else {
		fmt.Println("Results successfully saved to ingestion_throughput_results.txt")
	}
}
