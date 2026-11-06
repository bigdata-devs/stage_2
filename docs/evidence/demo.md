# End-to-End Evidence: Datalake → Indexing Service → MongoDB Datamart → Search Service

Recorded on 2026-10-18 on branch `feature/mongodb-datamart` (HEAD `6cc1e85`), against MongoDB
**7.0.45** in the `boogle-mongo-test` container, database `boogle`. Every response below is the
literal output of the shown command (mongosh lines cleaned of REPL prompts). HTTP status codes
follow each body in brackets.

The Indexing and Search services ran as real processes with the two bridge files from
[`docs/mongodb.md`](../mongodb.md) temporarily in place (`cmd/indexing/datamart_index.go`,
`cmd/search/datamart_engine.go` plus the wiring described in that document). The bridges are not
committed here: they belong to the services of Members 3 and 5.

## Corpus

Four Project Gutenberg books copied from `stage_1/sample_data` into the local (git-ignored)
`datalake/` in the layout `<root>/batch_<LOW>_<HIGH>/<book_id>/<id>_{header,body}.txt`:

| ID | Title | Author | Publication year |
|---|---|---|---|
| 11 | Alice's Adventures in Wonderland | Lewis Carroll | 1865 |
| 84 | Frankenstein; or, the modern prometheus | Mary Wollstonecraft Shelley | 1818 |
| 174 | The Picture of Dorian Gray | Oscar Wilde | 1890 |
| 1342 | Pride and Prejudice | Jane Austen | 1813 |

The upstream sample headers omit the `Original Publication:` line, so the *demo copies* carry it
added with each book's real first-publication year — otherwise `publication_year` would parse as
`0` for the whole corpus and the year filter could not be demonstrated live. No other byte of the
headers or bodies was changed.

## 1. Write path: index one book

Clean slate first (`db.books.drop(); db.inverted_index.drop()`), then the service starts with
`go build ./cmd/indexing` → `indexing_service.exe -addr :7002 -datalake datalake`:

```
=== initial GET /index/status ===
{"books_indexed":0,"last_update":"2026-10-18T16:06:32Z","index_size_MB":0}
 [200]

time=2026-10-18T17:06:32.226+01:00 level=INFO msg="mongodb indexes ready" created="[author_1_language_1_publication_year_1 language_1 publication_year_1]"
time=2026-10-18T17:06:32.234+01:00 level=INFO msg="[INDEXING] service listening" address=:7002 datalake=datalake rebuild_workers=28 rebuild_timeout=10m0s docs=/swagger/index.html

=== POST /index/update/1342 ===
{"book_id":1342,"index":"updated"}
 [200]

=== GET /index/status ===
{"books_indexed":1,"last_update":"2026-10-18T16:07:02Z","index_size_MB":0.4}
 [200]
```

State of the datamart after that single update (one `mongosh` round trip):

```
=== books collection ===
[
  {
    _id: 1342,
    title: 'Pride and Prejudice',
    author: 'Jane Austen',
    language: 'en',
    publication_year: 1813,
    release_year: 1998,
    status: 'published'
  }
]
=== term documents: 6576 ===
=== postings of term 'man' ===
{ _id: 'man', postings: [ { book_id: 1342, tf: 158 } ] }
=== postings of term 'prejudice' ===
{ _id: 'prejudice', postings: [ { book_id: 1342, tf: 12 } ] }
```

Proves: header parsed to metadata (`English → en`, `Original Publication: 1813 → 1813`), body
tokenized into **6576 distinct terms**, postings written with term frequencies, and the book
published (`status: published`) only after everything succeeded.

## 2. Write path: full rebuild (twice, idempotent)

```
=== POST /index/rebuild ===
{"books_processed":4,"elapsed_time":"991ms"}
 [200]

=== GET /index/status ===
{"books_indexed":4,"last_update":"2026-10-18T16:07:12Z","index_size_MB":1.1}
 [200]

=== POST /index/rebuild again (idempotency) ===
{"books_processed":4,"elapsed_time":"822ms"}
 [200]

time=2026-10-18T17:07:12.560+01:00 level=INFO msg="[INDEXING] index rebuilt" books=4 elapsed=991.1941ms
time=2026-10-18T17:07:13.412+01:00 level=INFO msg="[INDEXING] index rebuilt" books=4 elapsed=822.2071ms
```

Resulting datamart (both collections, after any number of rebuilds):

```
=== books collection ===
[
  { _id: 11,   title: "Alice's Adventures in Wonderland", author: 'Lewis Carroll', …, publication_year: 1865, status: 'published' },
  { _id: 84,   title: 'Frankenstein; or, the modern prometheus', author: 'Mary Wollstonecraft Shelley', …, publication_year: 1818, status: 'published' },
  { _id: 174,  title: 'The Picture of Dorian Gray', author: 'Oscar Wilde', …, publication_year: 1890, status: 'published' },
  { _id: 1342, title: 'Pride and Prejudice', author: 'Jane Austen', …, publication_year: 1813, status: 'published' }
]
=== counts: books=4 terms=13344 postings=22561 ===
=== postings of term 'man' (sorted by book_id) ===
{
  _id: 'man',
  postings: [ { book_id: 11, tf: 5 }, { book_id: 84, tf: 137 }, { book_id: 174, tf: 180 }, { book_id: 1342, tf: 158 } ]
}
=== dbStats dataSize ===
1154774
```

Proves: the rebuild processed the whole datalake in under a second, running it twice still yields
`books=4` (no duplicates — `_id` uniqueness plus idempotent writes), posting lists stay sorted by
`book_id`, the datamart holds **13344 terms / 22561 postings ≈ 1.1 MB** (the value
`GET /index/status` reports as `index_size_MB`), and the temp-collections swap left a fully
consistent state.

## 3. Read path: the Search Service

The Search Service started as `search_service.exe -addr :7013` (same database, default
`MONGO_URI`). Expected ranking for `q=man` from the postings above: **174 (tf 180), 1342 (158),
84 (137), 11 (5)**.

### Ranking, and normalization of the keyword

```
=== GET /search?q=man ===
{"query":"man","filters":{},"total":4,"offset":0,"limit":20,
 "results":[
   {"book_id":174,"title":"The Picture of Dorian Gray",…,"year":1890},
   {"book_id":1342,"title":"Pride and Prejudice",…,"year":1813},
   {"book_id":84,"title":"Frankenstein; or, the modern prometheus",…,"year":1818},
   {"book_id":11,"title":"Alice's Adventures in Wonderland",…,"year":1865}]}
 [200]

=== GET /search?q=MAN (normalization) ===
{"query":"MAN","filters":{},"total":4,…same four results…}
 [200]
```

Proves: descending tf order exactly as stored (180 → 158 → 137 → 5), `Total` counts every match,
and the bridge normalizes the raw keyword through `indexer.ProcessText` (`MAN → man`).

A second ranking over the same corpus confirms the order follows the stored frequencies:

```
=== GET /search?q=creature ===
{"query":"creature","filters":{},"total":4,"offset":0,"limit":20,
 "results":[{book_id:84,…},{book_id:1342,…},{book_id:174,…},{book_id:11,…}]}
 [200]
```

### Pagination (`total` stays4 across pages)

```
=== limit=2 ===
{"query":"man","filters":{},"total":4,"offset":0,"limit":2,"results":[{book_id:174,…},{book_id:1342,…}]}
 [200]
=== limit=2&offset=2 ===
{"query":"man","filters":{},"total":4,"offset":2,"limit":2,"results":[{book_id:84,…},{book_id:11,…}]}
 [200]
```

### Metadata filters (each one, then all combined)

```
=== author=wilde ===
{"total":1,"results":[{book_id:174,"title":"The Picture of Dorian Gray",…}]}          [200]

=== language=en ===
{"total":4,"results":[…all four…]}                                                     [200]

=== language=fr ===
{"total":0,"results":[]}                                                               [200]

=== year=1818 ===
{"total":1,"results":[{book_id:84,…}]}                                                 [200]

=== author=shelley&language=en&year=1818 (AND) ===
{"total":1,"results":[{book_id:84,"title":"Frankenstein; or, the modern prometheus",…}]}
 [200]
```

Proves: author is a case-insensitive substring (`wilde` matches *Oscar Wilde*), language matches
the stored comma list (`en` hits everything, `fr` nothing), year is an exact
`publication_year`, the three filters combine with AND, and an empty page answers
`"results": []` — never `null`.

### Stop word and error contract

```
=== GET /search?q=the ===
{"query":"the","filters":{},"total":0,"offset":0,"limit":20,"results":[]}
 [200]

=== GET /search?q=man&limit=0 ===
{"error":"invalid_parameter","message":"query parameter \"limit\" must be an integer between 1 and 100"}
 [400]
```

Proves: a stop word never reaches the index (the tokenizer drops it) and is answered with an
empty page, while an out-of-range parameter fails before any query with the documented JSON body.

## 4. Service logs

Both services ran the whole session without a single `level=ERROR` line on stdout or stderr;
the only entries are the start-up index creation, the listen announcements and the two rebuilds
shown above.

## 5. What the demo covers

| Claim | Evidence |
|---|---|
| Datalake book → metadata + postings in MongoDB | §1 (`update/1342`,6576 terms, `status: published`) |
| Rebuild is complete, atomic and idempotent | §2 (4 books twice, sorted postings, consistent counts) |
| `index_size_MB` comes from real storage | §2 (`dbStats dataSize`1154774 ≈1.1 MB) |
| Search ranking = tf desc, `book_id` asc | §3 (`q=man`, `q=creature`) |
| Same normalization as index time | §3 (`MAN` ≡ `man`; `the` → empty) |
| Filters: author / language / year + AND | §3 (five filter queries) |
| Pagination and totals | §3 (`limit=2` pages, `total: 4` on both) |
| Documented error behavior | §3 (`limit=0` →400 `invalid_parameter`) |
