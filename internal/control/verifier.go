package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"boogle-engine/internal/models"
)

var ErrBooksMissing = errors.New("indexed books are missing from the datamart")

type VerifyReport struct {
	Indexed int
	Missing []models.BookID
}

type Verifier struct {
	indexed IndexedBookLister
	search  SearchService
}

func NewVerifier(indexed IndexedBookLister, search SearchService) (Verifier, error) {
	if indexed == nil || search == nil {
		return Verifier{}, ErrMissingDependency
	}
	return Verifier{indexed: indexed, search: search}, nil
}

func (verifier Verifier) Verify(ctx context.Context) (VerifyReport, error) {
	indexed := verifier.indexed.IndexedIDs()
	report := VerifyReport{Indexed: len(indexed), Missing: []models.BookID{}}
	if len(indexed) == 0 {
		slog.Info("[CONTROL] verify: nothing indexed, nothing to reconcile")
		return report, nil
	}
	for _, bookID := range indexed {
		if err := ctx.Err(); err != nil {
			return report, fmt.Errorf("verify interrupted: %w", err)
		}
		published, err := verifier.search.IsPublished(ctx, bookID)
		if err != nil {
			return report, fmt.Errorf("check book %d in the search service: %w", bookID, err)
		}
		if !published {
			slog.Warn("[CONTROL] verify: book missing from datamart", "book_id", bookID)
			report.Missing = append(report.Missing, bookID)
		}
	}
	if len(report.Missing) > 0 {
		return report, fmt.Errorf("verify: %d of %d %w", len(report.Missing), len(indexed), ErrBooksMissing)
	}
	slog.Info("[CONTROL] verify: every indexed book is in the datamart", "verified", len(indexed))
	return report, nil
}
