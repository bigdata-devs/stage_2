package crawler

import "errors"

var ErrBookUnavailable = errors.New("book unavailable")

var ErrTransientDownload = errors.New("transient download failure")
