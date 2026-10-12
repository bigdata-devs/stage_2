// Package crawler downloads Project Gutenberg books, splits them into header and body and
// feeds them to the datalake through a concurrent worker pool. It is the Go port of the
// Stage 1 Python modules src/datalake/book_fetcher.py and src/datalake/errors.py.
package crawler

import "errors"

// ErrBookUnavailable marks a book that can never be downloaded, so retrying it is pointless:
// the ID does not exist (HTTP 404 and most other 4xx) or its text lacks the START/END markers.
var ErrBookUnavailable = errors.New("book unavailable")

// ErrTransientDownload marks a download that failed for a reason that may go away (timeouts,
// connection errors, HTTP 408/429 and 5xx), so the book should be retried later.
var ErrTransientDownload = errors.New("transient download failure")
