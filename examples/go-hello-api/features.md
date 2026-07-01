## Hello JSON API

Build a tiny HTTP API in this Go module (`demoapi`) using only the standard library.

Requirements:
- Add an exported function `NewServer() http.Handler` in a new file `server.go` that returns an `*http.ServeMux`.
- Register a single route `GET /hello` that responds with HTTP 200, header `Content-Type: application/json`, and the JSON body `{"message":"hello"}`.
- Add a table-driven test in `server_test.go` using `net/http/httptest` that asserts the status code, the `Content-Type` header, and the decoded JSON body for `GET /hello`.

Keep it minimal: no third-party dependencies, no main package required. Everything must pass `go vet ./...` and `go test ./...`.
