# Search Service catalog smoke (Member 4, Index Lead)

Captured 2026-10-19 20:08 from branch feature/indexing-service (MongoDB database `boogle_smoke`, two published books 5 and 1342).

```
curl -X GET http://localhost:7013/books/1342
HTTP 200
{"book_id":1342,"title":"Unknown","author":"Jane Austen","language":"en","year":1813}

curl -X GET http://localhost:7013/books/9999
HTTP 404
{"error":"book_not_found","message":"no published book with ID 9999"}

curl -X GET http://localhost:7013/books/abc
HTTP 400
{"error":"invalid_book_id","message":"path parameter \"book_id\" must be a positive integer, got \"abc\""}

curl -X GET http://localhost:7013/search/filters
HTTP 200
{"languages":["en"],"years":[1813,1905]}

curl -X POST http://localhost:7013/books/1342
HTTP 405

curl -X GET http://localhost:7013/search?q=adventure
HTTP 200
{"query":"adventure","filters":{},"total":5,"offset":0,"limit":20,"results":[{"book_id":11,"title":"Alice's Adventures in Wonderland","author":"Lewis Carroll","language":"en","year":1865},{"book_id":74,"title":"The Adventures of Tom Sawyer","author":"Mark Twain","language":"en","year":1876},{"book_id":76,"title":"Adventures of Huckleberry Finn","author":"Mark Twain","language":"en","year":1884},{"book_id":521,"title":"The Life and Adventures of Robinson Crusoe","author":"Daniel Defoe","language":"en","year":1719},{"book_id":1661,"title":"The Adventures of Sherlock Holmes","author":"Arthur Conan Doyle","language":"en","year":1892}]}

# Fail-fast: an unreachable datamart refuses the start-up instead of serving 500s
$ MONGO_URI=mongodb://127.0.0.1:9999/ go run ./cmd/search -addr :7014
exit code: 1
exit status 1
```
