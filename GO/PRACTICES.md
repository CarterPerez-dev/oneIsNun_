# Modern Go Best Practices Reference (2025)

**Purpose**: Guide for AI coding assistants to write idiomatic Go 1.21+ code. Focuses on recent changes and common AI mistakes from outdated training data.

---

## Language & Stdlib Changes (Go 1.21-1.23)

### Range Over Integers (Go 1.22+)
```go
// NEW: Range over integers
for i := range 10 { fmt.Println(i) }  // 0-9

// OLD: Classic for loop (still valid, but verbose)
for i := 0; i < 10; i++ { fmt.Println(i) }
```

### Loop Variable Semantics Fix (Go 1.22+)
**Critical change**: Each loop iteration now creates a new variable. The `v := v` capture pattern is obsolete.
```go
// Go 1.22+: Works correctly without workaround
for _, v := range values {
    go func() { fmt.Println(v) }()  // Each goroutine gets correct value
}

// OLD workaround (no longer needed): v := v
```

### Iterator Functions (Go 1.23+)
```go
// New iterator-based functions in slices/maps packages
sortedKeys := slices.Sorted(maps.Keys(m))  // Sort map keys
for i, v := range slices.Backward(s) { }   // Reverse iteration
chunks := slices.Chunk(s, 3)               // Chunk into groups
```

### New Stdlib Packages

**`log/slog` - Structured Logging (Go 1.21+)**
```go
// NEW: Structured logging (replaces log.Printf for structured needs)
slog.Info("user login", "user_id", 123, "ip", "192.168.1.1")

// JSON handler for production
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
logger.Info("request", slog.Int("status", 200), slog.Duration("latency", d))
```

**`slices` Package (Go 1.21+)** - Replaces `sort.Slice`
```go
slices.Sort(s)                    // In-place sort (~2.5x faster than sort.Ints)
slices.SortFunc(s, cmp.Compare)   // Custom comparator (returns int, not bool)
slices.Contains(s, v)             // No more manual contains loops
slices.Compact(s)                 // Remove consecutive duplicates
slices.Clone(s), slices.Reverse(s), slices.Insert(s, i, v...)
```

**`cmp.Or` - Zero Value Fallback (Go 1.22+)**
```go
// NEW: Elegant default value pattern
port := cmp.Or(os.Getenv("PORT"), "8080")

// Multi-field sorting
slices.SortFunc(items, func(a, b Item) int {
    return cmp.Or(cmp.Compare(a.Priority, b.Priority), cmp.Compare(a.Name, b.Name))
})
```

**Built-in `min`/`max`/`clear` (Go 1.21+)**
```go
m := max(x, 0)     // Ensure non-negative
clear(myMap)       // Delete all map entries
```

---

## Deprecated Patterns (AI Common Mistakes)

### `ioutil` Package (Deprecated Go 1.16)
| Deprecated | Modern Replacement |
|------------|-------------------|
| `ioutil.ReadFile()` | `os.ReadFile()` |
| `ioutil.ReadAll()` | `io.ReadAll()` |
| `ioutil.WriteFile()` | `os.WriteFile()` |
| `ioutil.TempFile()` | `os.CreateTemp()` |
| `ioutil.ReadDir()` | `os.ReadDir()` |

### Type Syntax
```go
// OLD: interface{} - still works but less readable
// NEW (Go 1.18+): any - preferred for new code
func process(data any) { }
```

### Sorting
```go
// OLD: sort.Slice with bool comparator
sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })

// NEW: slices.SortFunc with int comparator (-1, 0, 1)
slices.SortFunc(s, func(a, b int) int { return cmp.Compare(a, b) })
```

---

## Project Structure (2025 Conventions)

**Key insight**: The `golang-standards/project-layout` repo is **NOT official**. Go team lead Russ Cox called it inaccurate.

### Official Go Team Recommendations
- **Small projects**: Put code at root level. No `/pkg`, `/src` directories.
- **`/internal`**: Use liberally for private code (compiler-enforced).
- **`/cmd/appname/`**: Only when you have multiple executables.
- **`/pkg`**: Skip unless building genuinely reusable public libraries.

```
# Recommended structure for most projects
myproject/
  go.mod
  main.go           # or mypackage.go for libraries
  internal/
    auth/
    store/
  cmd/              # Only if multiple binaries
    server/
    cli/
```

**Avoid**: Deeply nested structures, generic names (`utils`, `helpers`, `common`), premature abstraction.

---

## HTTP/Web Patterns (Go 1.22+)

### Enhanced ServeMux Routing
```go
mux := http.NewServeMux()

// Method + path patterns (Go 1.22+)
mux.HandleFunc("GET /posts/{id}", getPost)
mux.HandleFunc("POST /posts", createPost)
mux.HandleFunc("GET /files/{path...}", serveFile)  // Catch-all
mux.HandleFunc("GET /posts/{$}", listPosts)        // Exact match only

// Path parameter extraction
func getPost(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
}
```

**When to use third-party routers (Chi, Echo)**: Only when you need middleware grouping, route groups, or regex matching. stdlib now handles 90% of cases.

---

## Error Handling (Modern Patterns)

### `errors.Join` (Go 1.20+)
```go
// Combine multiple errors
if err := errors.Join(err1, err2, err3); err != nil {
    return err  // errors.Is works on all joined errors
}
```

### Wrapping Best Practices
```go
// Add context when propagating
return fmt.Errorf("reading config: %w", err)

// Multiple wraps (Go 1.20+)
return fmt.Errorf("operation failed: %w, also: %w", err1, err2)

// Checking errors
if errors.Is(err, ErrNotFound) { }
var pathErr *os.PathError
if errors.As(err, &pathErr) { }
```

### Error Strategy (Uber Style Guide)
- **Sentinel errors**: Static errors callers match (`var ErrNotFound = errors.New("not found")`)
- **Custom types**: When caller needs to inspect fields
- **Wrapping**: Adding context up the stack

**Anti-pattern**: Never inspect `err.Error()` string content.

---

## Concurrency Patterns

### `sync.OnceFunc`/`OnceValue` (Go 1.21+)
```go
// Lazy initialization with caching
loadConfig := sync.OnceValue(func() *Config {
    return parseConfigFile()
})
cfg := loadConfig()  // Executes once, cached thereafter
```

### `errgroup` for Goroutine Management
```go
g, ctx := errgroup.WithContext(ctx)
g.SetLimit(10)  // Limit concurrent goroutines

for _, url := range urls {
    g.Go(func() error { return fetch(ctx, url) })
}
return g.Wait()  // Returns first error
```

### Common AI Concurrency Mistakes

**Goroutine leaks** - Always provide exit conditions:
```go
// BAD: No exit condition
go func() { for { doWork() } }()

// GOOD: Check context
go func(ctx context.Context) {
    for {
        select {
        case <-ctx.Done(): return
        default: doWork()
        }
    }
}(ctx)
```

**Channel mistakes**: Send to nil blocks forever, send to closed panics, close twice panics.

**Context handling**:
- Always pass `ctx` as first parameter
- Always `defer cancel()` after `context.WithTimeout/Cancel`
- Use `r.Context()` in HTTP handlers, pass to downstream calls

---

## File I/O & Embedding

### `//go:embed` (Go 1.16+)
```go
import "embed"

//go:embed static/*
var staticFiles embed.FS

//go:embed version.txt
var version string  // Single file as string

// Read embedded file
data, _ := staticFiles.ReadFile("static/index.html")
```

**Rules**: Must be package-level, directive immediately precedes variable, import `embed` package.

### JSON Handling
- `json.Unmarshal`: Data already in `[]byte`
- `json.NewDecoder(r).Decode(&v)`: Streaming from `io.Reader` (HTTP bodies) - more memory efficient
- Use `decoder.DisallowUnknownFields()` for strict parsing

---

## Key Sources
- Go Release Notes: go.dev/doc/go1.21, go1.22, go1.23
- Go Blog: go.dev/blog/routing-enhancements, go.dev/blog/slog
- Uber Go Style Guide: github.com/uber-go/guide
- Google Go Style Guide: google.github.io/styleguide/go
- Official Module Layout: go.dev/doc/modules/layout
