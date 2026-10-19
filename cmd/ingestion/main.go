package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"boogle-engine/internal/crawler"
	"boogle-engine/internal/datalake"
	"boogle-engine/internal/models"
)

type Server struct {
	lake     datalake.Datalake
	ingester crawler.Ingester
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	port := flag.Int("port", 7001, "Server port HTTP")
	lakeDir := flag.String("datalake", "datalake", "Datalake root directory")
	flag.Parse()

	lake, err := datalake.New(*lakeDir, datalake.DefaultBatchSize)
	if err != nil {
		slog.Error("Error starting data lake", "error", err)
		os.Exit(1)
	}

	client := crawler.NewGutenbergClient(crawler.DefaultBaseURL, crawler.DefaultRequestTimeout)
	retryPolicy := crawler.RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Second}
	ingester, err := crawler.NewIngester(client, lake, retryPolicy)
	if err != nil {
		slog.Error("Error starting ingester", "error", err)
		os.Exit(1)
	}

	server := &Server{lake: lake, ingester: ingester}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest/{book_id}", server.handleIngest)
	mux.HandleFunc("GET /ingest/status/{book_id}", server.handleStatus)
	mux.HandleFunc("GET /ingest/list", server.handleList)

	slog.Info("Ingest server started", "port", *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), mux); err != nil {
		slog.Error("The server stopped", "error", err)
	}
}

// POST /ingest/{book_id}
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	bookID, err := models.ParseBookID(r.PathValue("book_id"))
	if err != nil {
		http.Error(w, `{"error": "Invalid book ID"}`, http.StatusBadRequest)
		return
	}

	result := s.ingester.Ingest(context.Background(), bookID)

	w.Header().Set("Content-Type", "application/json")
	if result.Err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(result)
}

// GET /ingest/status/{book_id}
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	bookID, err := models.ParseBookID(r.PathValue("book_id"))
	if err != nil {
		http.Error(w, `{"error": "Invalid book ID"}`, http.StatusBadRequest)
		return
	}

	status := "unavailable"
	if _, err := s.lake.Locate(bookID); err == nil {
		status = "available"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"book_id": bookID,
		"status":  status,
	})
}

// GET /ingest/list
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	bookIDs, err := s.lake.ListBookIDs()
	if err != nil {
		http.Error(w, `{"error": "Error reading the datalake"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"count": len(bookIDs),
		"books": bookIDs,
	})
}
