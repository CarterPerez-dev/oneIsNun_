# Production sqlx patterns for Go Chi backends in 2025

**Modern Go backends using sqlx achieve the best balance of type safety, performance, and developer control.** This guide synthesizes current best practices for building production-ready, opinionated Go Chi templates suitable for mobile app backends (iOS/Android). The approaches here leverage Go 1.21+ features including generics, improved error handling, and embed directives—avoiding outdated patterns from earlier Go versions.

sqlx extends `database/sql` with powerful conveniences: struct scanning, named parameters, and IN clause expansion—while maintaining the explicit SQL control that production systems demand. Combined with Chi's middleware patterns, this stack provides excellent developer experience without sacrificing performance.

## Connection pooling determines your application's stability

**Misconfigured connection pools cause more production outages than any other database issue.** The critical settings are `SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime`, and `SetConnMaxIdleTime`—each serving a distinct purpose.

```go
func NewDatabase(dsn string) (*sqlx.DB, error) {
    db, err := sqlx.Connect("postgres", dsn)
    if err != nil {
        return nil, fmt.Errorf("database connection failed: %w", err)
    }
    
    db.SetMaxOpenConns(25)               // Hard limit on total connections
    db.SetMaxIdleConns(25)               // Keep connections warm
    db.SetConnMaxLifetime(5 * time.Minute)  // Force reconnection for credential rotation
    db.SetConnMaxIdleTime(5 * time.Minute)  // Reclaim unused connections
    
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := db.PingContext(ctx); err != nil {
        return nil, fmt.Errorf("database ping failed: %w", err)
    }
    
    return db, nil
}
```

The optimal pool size formula is `(core_count × 2) + effective_spindle_count`. For SSDs, spindle count equals 1, giving most servers **25-50 connections** as a starting point. Benchmark data shows dramatic differences: zero idle connections yield ~4.5ms operations while maintaining idle connections drops this to ~0.5ms—an **8x improvement**.

| Workload | MaxOpenConns | MaxIdleConns | ConnMaxLifetime |
|----------|--------------|--------------|-----------------|
| Small API | 25 | 25 | 5 min |
| Medium traffic | 50 | 25-30 | 5 min |
| High traffic | 100 | 20-30 | 30 min |
| Burst patterns | 100-200 | 50 | 5 min |

Never set `MaxIdleConns` higher than `MaxOpenConns`—Go auto-reduces it but this indicates misconfiguration. Always set `ConnMaxLifetime` below your database's connection timeout (MySQL defaults to 8 hours, PostgreSQL to infinite).

**Monitor pool statistics in production:**

```go
func exposePoolMetrics(db *sqlx.DB, registry *prometheus.Registry) {
    registry.MustRegister(prometheus.NewGaugeFunc(
        prometheus.GaugeOpts{Name: "db_open_connections"},
        func() float64 { return float64(db.Stats().OpenConnections) },
    ))
    registry.MustRegister(prometheus.NewGaugeFunc(
        prometheus.GaugeOpts{Name: "db_wait_count_total"},
        func() float64 { return float64(db.Stats().WaitCount) },
    ))
}
```

## Transaction patterns that prevent data corruption

**Always use `defer tx.Rollback()` immediately after `Begin`—it's a no-op after successful commit.** This pattern guarantees cleanup regardless of panics or early returns.

The recommended **functional transaction wrapper** centralizes error handling and prevents common mistakes:

```go
func InTx(ctx context.Context, db *sqlx.DB, fn func(tx *sqlx.Tx) error) error {
    tx, err := db.BeginTxx(ctx, nil)
    if err != nil {
        return fmt.Errorf("begin transaction: %w", err)
    }
    defer tx.Rollback()
    
    if err := fn(tx); err != nil {
        return err
    }
    return tx.Commit()
}

// Usage
err := InTx(ctx, db, func(tx *sqlx.Tx) error {
    if _, err := tx.ExecContext(ctx, "UPDATE accounts SET balance = balance - $1 WHERE id = $2", amount, fromID); err != nil {
        return err
    }
    _, err := tx.ExecContext(ctx, "UPDATE accounts SET balance = balance + $1 WHERE id = $2", amount, toID)
    return err
})
```

For cross-repository transactions, implement a **transaction provider pattern**:

```go
type TransactionProvider struct {
    db *sqlx.DB
}

func (p *TransactionProvider) Transact(ctx context.Context, fn func(adapters Adapters) error) error {
    return InTx(ctx, p.db, func(tx *sqlx.Tx) error {
        return fn(Adapters{
            Users:  NewUserRepository(tx),
            Audit:  NewAuditRepository(tx),
        })
    })
}
```

**Nested transactions require savepoint libraries** since `database/sql` doesn't support them natively. Use `github.com/heetch/sqalx` or `github.com/dhui/satomic` for this pattern. For isolation levels, prefer `sql.LevelRepeatableRead` over `FOR UPDATE` locks to reduce deadlock probability.

## Error handling distinguishes production code from prototypes

**Use `errors.Is()` and `errors.As()` for all database error checking.** Create domain-specific error types that wrap underlying database errors:

```go
var (
    ErrNotFound     = errors.New("record not found")
    ErrDuplicateKey = errors.New("duplicate key violation")
    ErrDeadlock     = errors.New("deadlock detected")
)

func (r *userRepo) GetByID(ctx context.Context, id int64) (*User, error) {
    var user User
    err := r.db.GetContext(ctx, &user, "SELECT * FROM users WHERE id = $1", id)
    if errors.Is(err, sql.ErrNoRows) {
        return nil, ErrNotFound
    }
    if err != nil {
        return nil, fmt.Errorf("get user %d: %w", id, err)
    }
    return &user, nil
}
```

For PostgreSQL, extract specific error codes:

```go
import "github.com/jackc/pgx/v5/pgconn"

var pgErr *pgconn.PgError
if errors.As(err, &pgErr) {
    switch pgErr.Code {
    case "23505": return ErrDuplicateKey      // unique_violation
    case "23503": return ErrForeignKey        // foreign_key_violation
    case "40P01": return ErrDeadlock          // deadlock_detected
    case "57014": return ErrQueryCanceled     // query_canceled
    }
}
```

**Implement retry logic for transient errors with exponential backoff:**

```go
func withRetry(fn func() error, maxAttempts int) error {
    var err error
    for attempt := 0; attempt < maxAttempts; attempt++ {
        err = fn()
        if err == nil || !isTransient(err) {
            return err
        }
        time.Sleep(time.Duration(attempt*attempt) * 100 * time.Millisecond)
    }
    return err
}

func isTransient(err error) bool {
    return errors.Is(err, ErrDeadlock) || 
           errors.Is(err, driver.ErrBadConn) ||
           strings.Contains(err.Error(), "connection reset")
}
```

## Query organization with go:embed provides the best developer experience

**Store SQL in separate files and embed at compile time.** This approach provides syntax highlighting, SQL linting, and single-binary deployment:

```go
package queries

import _ "embed"

//go:embed sql/users/get_by_id.sql
var GetUserByID string

//go:embed sql/users/create.sql
var CreateUser string
```

Organize queries by domain:

```
db/
├── queries/
│   ├── queries.go
│   └── sql/
│       ├── users/
│       │   ├── get_by_id.sql
│       │   ├── create.sql
│       │   └── list_active.sql
│       └── orders/
│           └── get_by_user.sql
└── repository/
    ├── user_repository.go
    └── order_repository.go
```

The **repository pattern** provides the cleanest abstraction:

```go
type UserRepository interface {
    GetByID(ctx context.Context, id int64) (*User, error)
    Create(ctx context.Context, user *User) (int64, error)
}

type userRepo struct {
    db DBTX  // Interface accepting both *sqlx.DB and *sqlx.Tx
}

//go:embed sql/users/get_by_id.sql
var getUserByIDQuery string

func (r *userRepo) GetByID(ctx context.Context, id int64) (*User, error) {
    var user User
    err := r.db.GetContext(ctx, &user, getUserByIDQuery, id)
    return &user, err
}
```

## Context usage prevents runaway queries and enables graceful cancellation

**Always use Context-aware methods** (`GetContext`, `SelectContext`, `ExecContext`). The request context enables automatic cancellation when clients disconnect:

```go
func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
    defer cancel()
    
    user, err := h.repo.GetByID(ctx, id)
    if err != nil {
        if errors.Is(err, context.DeadlineExceeded) {
            http.Error(w, "Request timeout", http.StatusGatewayTimeout)
            return
        }
        if errors.Is(err, context.Canceled) {
            return // Client disconnected
        }
        http.Error(w, "Internal error", http.StatusInternalServerError)
        return
    }
    json.NewEncoder(w).Encode(user)
}
```

Apply **tiered timeouts by query complexity**:

```go
const (
    FastQueryTimeout   = 3 * time.Second   // Simple lookups
    MediumQueryTimeout = 10 * time.Second  // Joins, aggregations
    LongQueryTimeout   = 30 * time.Second  // Reports, batch operations
)
```

## Chi integration patterns for mobile backends

**Inject the database via a server struct, not middleware context.** This approach is cleaner and more testable:

```go
type Server struct {
    db     *sqlx.DB
    router *chi.Mux
    cfg    *Config
}

func (s *Server) Routes() {
    s.router.Use(middleware.RequestID)
    s.router.Use(middleware.Recoverer)
    s.router.Use(middleware.Timeout(30 * time.Second))
    
    s.router.Get("/health", s.healthCheck)
    s.router.Route("/api/v1", func(r chi.Router) {
        r.Use(s.authMiddleware)
        r.Mount("/users", NewUserHandler(s.db).Routes())
    })
}

func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
    defer cancel()
    
    if err := s.db.PingContext(ctx); err != nil {
        w.WriteHeader(http.StatusServiceUnavailable)
        return
    }
    w.WriteHeader(http.StatusOK)
}
```

**Graceful shutdown closes HTTP before database:**

```go
func main() {
    db, _ := NewDatabase(os.Getenv("DATABASE_URL"))
    srv := &http.Server{Addr: ":8080", Handler: server.Routes()}
    
    ctx, stop := signal.NotifyContext(context.Background(), 
        syscall.SIGINT, syscall.SIGTERM)
    defer stop()
    
    go srv.ListenAndServe()
    
    <-ctx.Done()
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    
    srv.Shutdown(shutdownCtx)  // Stop accepting requests first
    db.Close()                  // Then close database
}
```

## Testing strategies that scale

**Use testcontainers-go for integration tests against real databases:**

```go
func SetupTestDB(t *testing.T) *sqlx.DB {
    ctx := context.Background()
    
    container, err := postgres.RunContainer(ctx,
        testcontainers.WithImage("postgres:16-alpine"),
        postgres.WithDatabase("test"),
        postgres.WithUsername("test"),
        postgres.WithPassword("test"),
        postgres.WithInitScripts("testdata/schema.sql"),
    )
    require.NoError(t, err)
    
    connStr, _ := container.ConnectionString(ctx, "sslmode=disable")
    db, _ := sqlx.Connect("postgres", connStr)
    
    t.Cleanup(func() {
        db.Close()
        container.Terminate(ctx)
    })
    
    return db
}
```

**For unit tests, use transaction rollback for isolation:**

```go
func TestTx(t *testing.T) *sqlx.Tx {
    db := getTestDB()
    tx, _ := db.Beginx()
    t.Cleanup(func() { tx.Rollback() })
    return tx
}

func TestUserCreate(t *testing.T) {
    tx := TestTx(t)
    repo := NewUserRepository(tx)
    
    user, err := repo.Create(context.Background(), &User{Name: "Test"})
    assert.NoError(t, err)
    assert.NotZero(t, user.ID)
    // Rollback happens automatically
}
```

Use **sqlmock for pure unit tests** when you need to verify exact queries:

```go
func TestGetUser(t *testing.T) {
    db, mock, _ := sqlmock.New()
    sqlxDB := sqlx.NewDb(db, "postgres")
    
    rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
    mock.ExpectQuery("SELECT .+ FROM users").WithArgs(1).WillReturnRows(rows)
    
    repo := NewUserRepository(sqlxDB)
    user, err := repo.GetByID(context.Background(), 1)
    
    assert.NoError(t, err)
    assert.Equal(t, "John", user.Name)
    assert.NoError(t, mock.ExpectationsWereMet())
}
```

## Security through parameterized queries and validation

**Every sqlx parameter method prevents SQL injection by design.** The driver sends parameters separately from query text, preventing structure modification:

```go
// Safe - parameters bound separately
db.GetContext(ctx, &user, "SELECT * FROM users WHERE id = $1", userID)
db.NamedExecContext(ctx, "INSERT INTO users (name) VALUES (:name)", user)

// DANGEROUS - string concatenation enables injection
db.Get(&user, fmt.Sprintf("SELECT * FROM users WHERE id = %s", userID))
```

**Validate input before queries even with parameterization:**

```go
func (r *userRepo) GetByEmail(ctx context.Context, email string) (*User, error) {
    if !emailRegex.MatchString(email) || len(email) > 255 {
        return nil, ErrInvalidEmail
    }
    
    var user User
    err := r.db.GetContext(ctx, &user, getUserByEmailQuery, email)
    return &user, err
}
```

**Store credentials in environment variables or secret managers**, never in code. Use separate database users for application (limited permissions) and migrations (full permissions).

## Advanced sqlx features worth knowing

**`sqlx.In` expands slices for IN clauses:**

```go
ids := []int{1, 2, 3, 4, 5}
query, args, _ := sqlx.In("SELECT * FROM users WHERE id IN (?)", ids)
query = db.Rebind(query)  // Convert ? to $1, $2... for PostgreSQL

var users []User
db.SelectContext(ctx, &users, query, args...)
```

**Custom types for JSONB columns:**

```go
type Metadata map[string]interface{}

func (m Metadata) Value() (driver.Value, error) { return json.Marshal(m) }
func (m *Metadata) Scan(v interface{}) error { return json.Unmarshal(v.([]byte), m) }

type User struct {
    ID       int      `db:"id"`
    Metadata Metadata `db:"metadata"`  // Automatically marshaled/unmarshaled
}
```

**RETURNING clause captures auto-generated fields:**

```go
var user User
err := db.GetContext(ctx, &user, `
    INSERT INTO users (name, email) VALUES ($1, $2) 
    RETURNING id, name, email, created_at
`, name, email)
```

**`MapScan` for dynamic column handling:**

```go
rows, _ := db.Queryx("SELECT * FROM dynamic_table")
for rows.Next() {
    result := make(map[string]interface{})
    rows.MapScan(result)
    // result contains all columns without struct definition
}
```

## Common anti-patterns that cause production incidents

**Creating connections per request exhausts database resources:**

```go
// WRONG - creates new pool every request
func handler(w http.ResponseWriter, r *http.Request) {
    db, _ := sqlx.Connect("postgres", dsn)
    defer db.Close()
}

// CORRECT - reuse single pool
var db *sqlx.DB

func init() {
    db, _ = sqlx.Connect("postgres", dsn)
    db.SetMaxOpenConns(25)
}
```

**Forgetting to close rows leaks connections:**

```go
// WRONG - connection never returned to pool
rows, _ := db.Queryx("SELECT * FROM users")
for rows.Next() { /* ... */ }

// CORRECT - always close or use Get/Select
rows, _ := db.Queryx("SELECT * FROM users")
defer rows.Close()
for rows.Next() { /* ... */ }
```

**Using Select for unbounded queries causes memory exhaustion:**

```go
// WRONG - loads entire table into memory
var users []User
db.Select(&users, "SELECT * FROM users")  // 10M rows = OOM

// CORRECT - stream with Queryx for large datasets
rows, _ := db.Queryx("SELECT * FROM users")
defer rows.Close()
for rows.Next() {
    var user User
    rows.StructScan(&user)
    processUser(user)  // Process without accumulating
}
```

## Migration strategies with goose or Atlas

**Goose provides simple, embedded migrations:**

```go
import (
    "embed"
    "github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(db *sql.DB) error {
    goose.SetBaseFS(migrations)
    goose.SetDialect("postgres")
    return goose.Up(db, "migrations")
}
```

Migration files use clear markers:

```sql
-- +goose Up
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    created_at TIMESTAMP DEFAULT NOW()
);

-- +goose Down
DROP TABLE users;
```

**For zero-downtime migrations, follow expand-contract patterns:** add new columns with defaults, migrate data, update code, then remove old columns in a subsequent release.

## Conclusion

Building production-ready sqlx backends requires attention to **connection pooling, transaction safety, error handling, and proper testing infrastructure**. The patterns here—functional transaction wrappers, repository interfaces, embedded SQL files, and testcontainers integration—provide a solid foundation for mobile backend development.

Key decisions that distinguish professional implementations: always configure pool limits explicitly, use Context for every database operation, wrap errors with domain context, and test against real databases. These patterns scale from startup MVPs to high-traffic production systems without requiring architectural rewrites.

The combination of Chi's clean routing with sqlx's explicit SQL control creates backends that are both maintainable and performant—essential qualities for mobile apps where API response times directly impact user experience.
