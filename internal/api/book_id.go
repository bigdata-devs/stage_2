package api

import (
	"fmt"
	"net/http"

	"boogle-engine/internal/models"
)

const bookIDPathParameter = "book_id"

const (
	InvalidBookIDCode = "invalid_book_id"
	BookNotFoundCode  = "book_not_found"
)

func ParseBookIDPath(request *http.Request) (models.BookID, error) {
	rawBookID := request.PathValue(bookIDPathParameter)
	bookID, err := models.ParseBookID(rawBookID)
	if err != nil {
		return 0, fmt.Errorf("path parameter %q must be a positive integer, got %q", bookIDPathParameter, rawBookID)
	}
	return bookID, nil
}

func WriteInvalidBookID(writer http.ResponseWriter, err error) {
	WriteJSON(writer, http.StatusBadRequest, ErrorResponse{Error: InvalidBookIDCode, Message: err.Error()})
}
