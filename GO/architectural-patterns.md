# Modern Go architectural patterns for production systems in 2024-2025

The Go ecosystem has matured significantly, with clear consensus emerging on architectural best practices. **For your MongoDB Management Dashboard**, the recommended approach combines Clean Architecture principles, Uber fx for dependency injection with lifecycle management, interface-based design for testability, and context-driven graceful shutdown. This report covers project structure, dependency injection, error handling, graceful shutdown, and interface patterns—all with specific guidance for applications managing HTTP servers, WebSocket connections, MongoDB, SQLite, and scheduled jobs.

## Project structure has evolved away from rigid templates

The Go community has firmly rejected the notion of a single "standard" layout. **The golang-standards/project-layout repository is not official**—Go team lead Russ Cox explicitly stated this in GitHub Issue #117, noting that "the vast majority of packages in the Go ecosystem do not put importable packages in a pkg subdirectory." The official reference is now go.dev/doc/modules/layout, which recommends progressive complexity.

For a complex application with multiple concerns, the recommended structure follows Clean Architecture / Hexagonal Architecture principles:

```
mongodb-dashboard/
├── cmd/
│   └── server/
│       └── main.go              # Wires everything together
├── internal/
│   ├── domain/                  # Pure business types (no external deps)
│   │   ├── metrics.go           # Health metrics, slow query types
│   │   └── backup.go            # Backup history types
│   ├── app/                     # Application layer (use cases)
│   │   ├── metrics_service.go   # Queries/sec, connections logic
│   │   └── backup_service.go    # Backup orchestration
│   ├── adapters/
│   │   ├── mongodb/             # MongoDB repository implementations
│   │   │   └── metrics_repo.go
│   │   ├── sqlite/              # SQLite backup history
│   │   │   └── backup_repo.go
│   │   └── mongodump/           # mongodump executor
│   │       └── executor.go
│   └── ports/
│       ├── http/                # REST API handlers
│       │   └── handler.go
│       ├── ws/                  # WebSocket for real-time metrics
│       │   └── hub.go
│       └── scheduler/           # Scheduled mongodump jobs
│           └── jobs.go
├── config/
└── go.mod
```

The **internal/ directory is officially supported** by the Go compiler, which prevents external packages from importing its contents. Use it liberally for business logic, adapters, and domain code. The **pkg/ directory is controversial**—it provides no special compiler treatment and often adds unnecessary nesting. Skip it unless you have reusable code genuinely intended for external consumption.

The **dependency rule** in Clean Architecture mandates that outer layers (adapters) can import inner layers (domain/app), but never the reverse. Inner layers define interfaces that outer layers implement, enabling the domain to remain pure and testable without database dependencies.

## Dependency injection approaches range from manual to fully managed

Go offers three primary DI approaches: **manual constructor injection** (idiomatic), **Google Wire** (compile-time code generation), and **Uber Dig/fx** (runtime reflection). For your dashboard with its multiple database connections, scheduled jobs, and WebSocket server, **Uber fx emerges as the strongest choice** due to its lifecycle management capabilities.

### Manual constructor injection remains the Go idiom

The standard pattern passes dependencies via `NewXxx` constructor functions:

```go
type MetricsService struct {
    mongoRepo MongoMetricsRepository
    logger    *slog.Logger
}

func NewMetricsService(repo MongoMetricsRepository, logger *slog.Logger) *MetricsService {
    return &MetricsService{mongoRepo: repo, logger: logger}
}
```

This approach offers **compile-time safety, zero runtime overhead, and explicit wiring**. However, as applications grow past ~150 lines of wiring code in main.go, the repetitive nature becomes burdensome. Manual DI works excellently for projects with fewer than 10 services.

### Google Wire generates wiring code at compile time

Wire analyzes provider functions and generates the dependency graph:

```go
//go:build wireinject

var AppSet = wire.NewSet(
    NewConfig,
    NewLogger,
    NewMongoClient,
    NewMetricsRepository,
    NewMetricsService,
)

func InitializeApp() (*MetricsService, error) {
    wire.Build(AppSet)
    return nil, nil // Wire replaces this
}
```

Running `wire` generates human-readable Go code with proper error handling. **Wire catches missing dependencies at compile time** and produces zero-reflection code. The tradeoff is requiring an extra build step and generated files that add repository noise.

### Uber fx provides full lifecycle management

For applications with complex startup/shutdown requirements—HTTP servers, database connections, WebSocket hubs, scheduled jobs—**fx adds lifecycle hooks that simplify graceful shutdown**:

```go
func main() {
    fx.New(
        fx.Provide(
            NewConfig,
            NewLogger,
            NewMongoClient,
            NewSQLiteDB,
            NewMetricsRepository,
            NewBackupRepository,
            NewMetricsService,
            NewBackupService,
        ),
        fx.Invoke(RegisterHTTPServer),
        fx.Invoke(RegisterWebSocket),
        fx.Invoke(RegisterScheduledJobs),
    ).Run()
}

func NewMongoClient(lc fx.Lifecycle, cfg *Config, logger *zap.Logger) (*mongo.Client, error) {
    client, err := mongo.Connect(context.Background(), options.Client().ApplyURI(cfg.MongoURI))
    if err != nil {
        return nil, err
    }
    
    lc.Append(fx.Hook{
        OnStart: func(ctx context.Context) error {
            logger.Info("Connecting to MongoDB")
            return client.Ping(ctx, nil)
        },
        OnStop: func(ctx context.Context) error {
            logger.Info("Disconnecting from MongoDB")
            return client.Disconnect(ctx)
        },
    })
    return client, nil
}
```

The **OnStart and OnStop hooks execute in dependency order**, ensuring MongoDB connects before services that need it and disconnects after they stop. fx handles SIGTERM/SIGINT automatically and provides observable startup sequences for debugging. The tradeoff is runtime reflection overhead (microseconds at startup) and framework lock-in.

### Decision matrix for your MongoDB Dashboard

| Factor | Recommendation |
|--------|----------------|
| Multiple database connections (MongoDB + SQLite) | fx—lifecycle hooks manage connections cleanly |
| Scheduled jobs (mongodump) | fx—OnStop waits for running jobs |
| WebSocket connections | fx—coordinated shutdown drains connections |
| HTTP server graceful shutdown | fx—handles automatically |
| Team size/familiarity | If unfamiliar with DI libraries, start manual, migrate to fx later |

## Error handling follows strict conventions in modern Go

The Go 1.13+ error wrapping model has matured into clear best practices. **Panic is reserved exclusively for programmer errors**—situations that "should never happen" like invalid hardcoded regex or nil pointer dereferences. Libraries must never panic; always return errors.

### Wrap errors with context using %w

```go
func (r *MetricsRepository) GetQueriesPerSecond(ctx context.Context) (float64, error) {
    result := r.db.RunCommand(ctx, bson.D{{"serverStatus", 1}})
    if err := result.Err(); err != nil {
        return 0, fmt.Errorf("fetching server status: %w", err)
    }
    // Parse result...
    return qps, nil
}
```

The `%w` verb preserves the original error for inspection with `errors.Is()` and `errors.As()`. Use `%v` instead when you want to hide implementation details—for instance, don't expose `sql.ErrNoRows` from your repository; return a domain error like `ErrNotFound`.

### Handle or return errors, never both

A critical anti-pattern is logging an error and also returning it, causing duplicate logs:

```go
// BAD: Logs AND returns, causing duplicate logs up the stack
if err := step(); err != nil {
    log.Printf("step failed: %v", err)
    return err  // Caller will also log this
}

// GOOD: Return with context, log at the boundary
if err := step(); err != nil {
    return fmt.Errorf("processing step: %w", err)
}

// At HTTP handler boundary (top level):
if err := s.process(r); err != nil {
    slog.Error("request failed", "error", err, "path", r.URL.Path)
    http.Error(w, "Internal Error", 500)
}
```

### Sentinel errors and custom types enable precise error handling

Define sentinel errors for conditions callers need to check:

```go
var (
    ErrNotFound      = errors.New("not found")
    ErrSlowQuery     = errors.New("slow query threshold exceeded")
    ErrBackupFailed  = errors.New("backup failed")
)

// Usage with wrapping
return fmt.Errorf("metrics for %s: %w", collName, ErrNotFound)

// Checking
if errors.Is(err, ErrNotFound) {
    http.Error(w, "Not Found", http.StatusNotFound)
}
```

Custom error types carry additional context:

```go
type QueryError struct {
    Query    string
    Duration time.Duration
    Err      error
}

func (e *QueryError) Error() string {
    return fmt.Sprintf("query %q took %v: %v", e.Query, e.Duration, e.Err)
}

func (e *QueryError) Unwrap() error { return e.Err }

// Extracting with errors.As
var queryErr *QueryError
if errors.As(err, &queryErr) {
    logger.Warn("slow query detected", "query", queryErr.Query, "duration", queryErr.Duration)
}
```

**Note**: The pkg/errors package from Dave Cheney is in maintenance mode and effectively deprecated. Use the standard library for all new code.

## Graceful shutdown requires coordinated multi-phase teardown

Production Go applications must handle SIGTERM (sent by Kubernetes before forced shutdown) and SIGINT (Ctrl+C). The modern approach uses `signal.NotifyContext` from Go 1.16+:

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

// Start services...

<-ctx.Done()
stop()  // Allow second Ctrl+C to force exit
```

### Shutdown phases must execute in correct order

For your dashboard with HTTP, WebSocket, MongoDB, SQLite, and scheduled jobs:

```go
const (
    shutdownTimeout     = 30 * time.Second
    readinessDrainDelay = 5 * time.Second
)

func (app *Application) Shutdown(ctx context.Context) error {
    // Phase 1: Stop accepting new work
    app.isShuttingDown.Store(true)      // Fail health checks
    cronCtx := app.cron.Stop()           // No new scheduled jobs
    time.Sleep(readinessDrainDelay)      // Let load balancer detect
    
    // Phase 2: Drain existing work
    app.wsHub.CloseAllConnections()      // Close WebSocket clients
    
    if err := app.httpServer.Shutdown(ctx); err != nil {
        return fmt.Errorf("HTTP shutdown: %w", err)
    }
    
    // Wait for scheduled jobs to complete
    select {
    case <-cronCtx.Done():
    case <-ctx.Done():
        return ctx.Err()
    }
    
    // Phase 3: Release resources (reverse order of initialization)
    if err := app.mongo.Disconnect(ctx); err != nil {
        log.Printf("MongoDB disconnect error: %v", err)
    }
    
    if err := app.sqlite.Close(); err != nil {
        log.Printf("SQLite close error: %v", err)
    }
    
    return nil
}
```

### HTTP server shutdown is built-in

The `http.Server.Shutdown()` method immediately stops accepting new connections and waits for existing requests to complete:

```go
server := &http.Server{
    Addr:         ":8080",
    Handler:      mux,
    ReadTimeout:  15 * time.Second,
    WriteTimeout: 30 * time.Second,
}

go func() {
    if err := server.ListenAndServe(); err != http.ErrServerClosed {
        log.Fatalf("Server error: %v", err)
    }
}()

// On shutdown signal
shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

if err := server.Shutdown(shutdownCtx); err != nil {
    log.Printf("Graceful shutdown failed: %v", err)
    server.Close()  // Force close
}
```

### Scheduled jobs require careful handling

Using robfig/cron v3, the `Stop()` method returns a context that's canceled when all running jobs complete:

```go
c := cron.New(cron.WithSeconds())
c.AddFunc("0 */6 * * *", func() {
    select {
    case <-shutdownCtx.Done():
        return  // Don't start if shutting down
    default:
        runMongodump(shutdownCtx)
    }
})
c.Start()

// On shutdown
cronDoneCtx := c.Stop()  // Stop scheduling, returns when jobs finish

select {
case <-cronDoneCtx.Done():
    log.Println("All cron jobs completed")
case <-time.After(60 * time.Second):
    log.Println("Timeout waiting for cron jobs")
}
```

### WebSocket connections need graceful close messages

```go
func (h *Hub) CloseAllConnections() {
    closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutting down")
    
    h.mu.RLock()
    for conn := range h.connections {
        conn.WriteControl(websocket.CloseMessage, closeMsg, time.Now().Add(5*time.Second))
    }
    h.mu.RUnlock()
    
    // Wait briefly for clients to acknowledge
    time.Sleep(2 * time.Second)
    
    h.mu.Lock()
    for conn := range h.connections {
        conn.Close()
    }
    h.mu.Unlock()
}
```

## Interface design follows Go's unique implicit satisfaction model

Go interfaces differ fundamentally from other languages: types implement interfaces implicitly by having the required methods. This enables **defining interfaces at the consumer level**, not where types are implemented.

### Accept interfaces, return structs

This principle promotes loose coupling:

```go
// Consumer defines what it needs (in app layer)
type MetricsStore interface {
    GetQueriesPerSecond(ctx context.Context) (float64, error)
    GetActiveConnections(ctx context.Context) (int, error)
    GetSlowQueries(ctx context.Context, threshold time.Duration) ([]SlowQuery, error)
}

// Service accepts interface, returns concrete type
func NewMetricsService(store MetricsStore, logger *slog.Logger) *MetricsService {
    return &MetricsService{store: store, logger: logger}
}
```

Benefits: swap MongoDB for a test double without changing service code, and add methods to implementations without breaking consumers.

### Keep interfaces small and focused

Dave Cheney emphasizes: "Well designed interfaces are more likely to be small interfaces; the prevailing idiom is an interface contains only a single method." The io.Reader pattern exemplifies this:

```go
// Good: Focused interfaces
type MetricsReader interface {
    GetQueriesPerSecond(ctx context.Context) (float64, error)
}

type BackupExecutor interface {
    Execute(ctx context.Context, dbName string) error
}

// Compose when needed
type MetricsService interface {
    MetricsReader
    ConnectionChecker
    SlowQueryAnalyzer
}
```

Avoid "god interfaces" that force implementers to provide methods they don't need. If a handler only reads metrics, it should accept `MetricsReader`, not a full repository interface.

### Repository pattern separates domain from infrastructure

Define repository interfaces in the domain/app layer:

```go
// In internal/domain or internal/app
type BackupRepository interface {
    Save(ctx context.Context, backup *Backup) error
    GetByID(ctx context.Context, id string) (*Backup, error)
    ListRecent(ctx context.Context, limit int) ([]*Backup, error)
}
```

Implement in infrastructure/adapters:

```go
// In internal/adapters/sqlite
type SQLiteBackupRepository struct {
    db *sql.DB
}

func NewSQLiteBackupRepository(db *sql.DB) *SQLiteBackupRepository {
    return &SQLiteBackupRepository{db: db}
}

func (r *SQLiteBackupRepository) Save(ctx context.Context, b *Backup) error {
    _, err := r.db.ExecContext(ctx,
        "INSERT INTO backups (id, database_name, path, created_at, size_bytes, status) VALUES (?, ?, ?, ?, ?, ?)",
        b.ID, b.DatabaseName, b.Path, b.CreatedAt, b.SizeBytes, b.Status)
    return err
}
```

### Mocking strategies for testing

For simple interfaces, manual mocks suffice:

```go
type MockMetricsStore struct {
    QPS         float64
    Connections int
    Err         error
}

func (m *MockMetricsStore) GetQueriesPerSecond(ctx context.Context) (float64, error) {
    return m.QPS, m.Err
}

func TestMetricsService(t *testing.T) {
    mock := &MockMetricsStore{QPS: 150.5}
    service := NewMetricsService(mock, slog.Default())
    
    qps, err := service.GetCurrentQPS(context.Background())
    assert.NoError(t, err)
    assert.Equal(t, 150.5, qps)
}
```

For complex interfaces or many implementations, use **mockgen** (now maintained by Uber at go.uber.org/mock) or **mockery**. GoMock provides expectation-based verification:

```go
func TestWithGoMock(t *testing.T) {
    ctrl := gomock.NewController(t)
    defer ctrl.Finish()
    
    mockRepo := mocks.NewMockBackupRepository(ctrl)
    mockRepo.EXPECT().
        ListRecent(gomock.Any(), 10).
        Return([]*Backup{{ID: "1", Status: "completed"}}, nil)
    
    service := NewBackupService(mockRepo)
    backups, _ := service.GetRecentBackups(context.Background())
    assert.Len(t, backups, 1)
}
```

## Putting it all together for your MongoDB Dashboard

Based on these patterns, here's a recommended architecture for your specific requirements:

**Project structure**: Use Clean Architecture with `internal/domain`, `internal/app`, `internal/adapters/{mongodb,sqlite,mongodump}`, and `internal/ports/{http,ws,scheduler}`.

**Dependency injection**: Use Uber fx for lifecycle-managed dependencies. The OnStart/OnStop hooks handle MongoDB connection pooling, SQLite initialization, HTTP server startup, WebSocket hub management, and cron job scheduling—all with coordinated graceful shutdown.

**Error handling**: Return wrapped errors from repositories and services; log only at HTTP handler boundaries. Define domain errors like `ErrDatabaseUnreachable` and `ErrBackupInProgress` for conditions callers need to handle.

**Graceful shutdown**: Implement three-phase shutdown: fail health checks and stop accepting new WebSocket connections, drain existing HTTP requests and wait for running mongodump jobs, then close MongoDB and SQLite connections in reverse order.

**Interface design**: Define repository interfaces in `internal/app` (not `internal/adapters`). Keep interfaces minimal—`MetricsReader` for real-time dashboard, `BackupExecutor` for the scheduler, `BackupRepository` for history queries. Use manual mocks for unit tests; reserve mockgen for complex integration scenarios.

This architecture provides clean separation of concerns, straightforward testing, and production-ready lifecycle management—precisely what a monitoring dashboard with real-time updates and scheduled operations requires.
