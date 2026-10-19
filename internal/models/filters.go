package models

// FilterValues lists the distinct values the metadata filters of the Search API accept over the
// published books: ISO 639 language codes, flattened from the comma-separated language lists, and
// original publication years without a known value. Both slices are sorted ascending and are
// never nil, so a response built from them always serializes as JSON arrays, never null.
type FilterValues struct {
	Languages []string `json:"languages"`
	Years     []int    `json:"years"`
}
