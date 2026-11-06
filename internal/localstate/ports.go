package localstate

import "boogle-engine/internal/control"

var (
	_ control.ProgressStore     = (*Store)(nil)
	_ control.IndexedBookLister = (*Store)(nil)
	_ control.FailureResetter   = (*Store)(nil)
)
