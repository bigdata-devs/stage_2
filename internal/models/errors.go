package models

import "errors"

var (
	ErrBookNotFound    = errors.New("book not found")
	ErrBookUnavailable = errors.New("book unavailable")
	ErrTransient       = errors.New("transient failure")
	ErrPermanent       = errors.New("permanent failure")
)
