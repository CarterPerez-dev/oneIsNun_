# Building a production MongoDB management dashboard in Go

A MongoDB management dashboard combining real-time metrics, backup automation, and a React frontend requires careful architectural decisions across **10 distinct technical domains**. This guide synthesizes production-quality patterns for Go project structure, dependency injection, MongoDB monitoring, SQLite storage, scheduled jobs, safe command execution, and WebSocket-based real-time updates.

## Project structure follows clean architecture principles

The most production-proven approach for Go web applications uses Clean/Hexagonal Architecture with clear separation between domain logic, application services, and infrastructure adapters:

```
mongodb-dashboard/
├── cmd/
│   └── dashboard/
│       └── main.go                 # Entry point, DI wiring
├── internal/
│   ├── domain/                     # Pure business logic
│   │   ├── backup.go               # Backup entity
│   │   ├── metrics.go              # Metrics value objects
│   │   └── database.go             # Database entity
│   ├── application/                # Use cases / orchestration
│   │   ├── backup_service.go       # Backup operations
│   │   ├── metrics_service.go      # Metrics aggregation
│   │   └── scheduler_service.go    # Job scheduling
│   ├── ports/                      # Entry points
│   │   ├── http/
│   │   │   ├── handler.go          # REST handlers
│   │   │   ├── middleware.go
│   │   │   └── router.go
│   │   └── websocket/
│   │       ├── hub.go              # Client management
│   │       └── handler.go          # WS upgrade handler
│   ├── adapters/                   # Infrastructure implementations
│   │   ├── mongo/
│   │   │   ├── client.go           # Connection management
│   │   │   └── health_repository.go
│   │   ├── sqlite/
│   │   │   ├── migrations/
│   │   │   └── backup_repository.go
│   │   └── backup/
│   │       └── executor.go         # mongodump wrapper
│   └── config/
│       └── config.go               # Configuration loading
├── configs/
│   └── config.yaml
├── go.mod
└── go.sum
```

The **dependency rule** flows inward: adapters and ports import application layer, application imports domain, but domain knows nothing about outer layers. The `internal/` directory enforces encapsulation—Go prevents external packages from importing it.

## Dependency injection works best with manual wiring

For a medium-complexity dashboard, **manual dependency injection** is the Go-idiomatic approach. The community consensus is to start manual and only adopt frameworks when boilerplate becomes unmanageable (typically 30+ dependencies).

```go
// cmd/dashboard/main.go
func main() {
    cfg := config.Load()
    
    // Create adapters (infrastructure layer)
    mongoClient := mongo.NewClient(cfg.MongoURI)
    sqliteDB := sqlite.Open(cfg.SQLitePath)
    
    // Create repositories
    healthRepo := mongo.NewHealthRepository(mongoClient)
    backupRepo := sqlite.NewBackupRepository(sqliteDB)
    
    // Create services (application layer)
    metricsService := application.NewMetricsService(healthRepo)
    backupExecutor := backup.NewExecutor(cfg.BackupDir)
    backupService := application.NewBackupService(backupRepo, backupExecutor)
    
    // Create scheduler
    scheduler := application.NewScheduler(backupService)
    
    // Create WebSocket hub
    hub := websocket.NewHub()
    go hub.Run(context.Background())
    
    // Create HTTP handlers (ports layer)
    handler := http.NewHandler(metricsService, backupService, hub)
    
    // Start server
    server := http.NewServer(handler, cfg.Port)
    server.Run()
}
```

The critical Go idiom is **"Accept interfaces, return structs"**—define interfaces at the consumer level, not the producer:

```go
// internal/application/metrics_service.go

// Interface defined where it's USED, not where it's implemented
type healthRepository interface {
    GetServerStatus(ctx context.Context) (*domain.ServerStatus, error)
    GetCurrentOps(ctx context.Context) ([]domain.Operation, error)
}

type MetricsService struct {
    repo healthRepository  // Accept interface
}

func NewMetricsService(repo healthRepository) *MetricsService {
    return &MetricsService{repo: repo}  // Return concrete struct
}
```

This pattern enables easy testing (mock the interface) and decoupling (repository doesn't know about service's interface requirements).

| DI Approach | Compile-time Safety | Best For |
|-------------|---------------------|----------|
| Manual DI | Full | Small-medium apps, maximum clarity |
| Google Wire | Full (code generation) | Large apps, reduced boilerplate |
| Uber Fx | Runtime errors | Microservices needing lifecycle management |

## MongoDB health monitoring uses serverStatus and $currentOp

The Go MongoDB driver executes admin commands via `RunCommand()` on the admin database. The **serverStatus** command provides comprehensive metrics:

```go
func (r *HealthRepository) GetServerStatus(ctx context.Context) (*domain.ServerStatus, error) {
    ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
    defer cancel()
    
    adminDB := r.client.Database("admin")
    
    var status struct {
        Connections struct {
            Current      int64 `bson:"current"`
            Available    int64 `bson:"available"`
            TotalCreated int64 `bson:"totalCreated"`
        } `bson:"connections"`
        Opcounters struct {
            Insert  int64 `bson:"insert"`
            Query   int64 `bson:"query"`
            Update  int64 `bson:"update"`
            Delete  int64 `bson:"delete"`
            Command int64 `bson:"command"`
        } `bson:"opcounters"`
        Mem struct {
            Resident int64 `bson:"resident"`
            Virtual  int64 `bson:"virtual"`
        } `bson:"mem"`
        Uptime int64 `bson:"uptime"`
    }
    
    cmd := bson.D{{"serverStatus", 1}}
    if err := adminDB.RunCommand(ctx, cmd).Decode(&status); err != nil {
        return nil, fmt.Errorf("serverStatus failed: %w", err)
    }
    
    return &domain.ServerStatus{
        ActiveConnections: status.Connections.Current,
        AvailableConnections: status.Connections.Available,
        QueriesPerSec: status.Opcounters.Query,
        // ... map other fields
    }, nil
}
```

For **slow query detection**, enable the database profiler and query `system.profile`:

```go
func (r *HealthRepository) GetSlowQueries(ctx context.Context, dbName string, minMillis int) ([]domain.SlowQuery, error) {
    collection := r.client.Database(dbName).Collection("system.profile")
    
    filter := bson.D{{"millis", bson.D{{"$gt", minMillis}}}}
    opts := options.Find().SetSort(bson.D{{"ts", -1}}).SetLimit(50)
    
    cursor, err := collection.Find(ctx, filter, opts)
    if err != nil {
        return nil, err
    }
    defer cursor.Close(ctx)
    
    var queries []domain.SlowQuery
    return queries, cursor.All(ctx, &queries)
}
```

For **current operations**, use the `$currentOp` aggregation stage (recommended over the deprecated `currentOp` command for MongoDB 5.0+):

```go
func (r *HealthRepository) GetCurrentOps(ctx context.Context) ([]bson.M, error) {
    pipeline := bson.A{
        bson.D{{"$currentOp", bson.D{
            {"allUsers", true},
            {"idleConnections", false},
        }}},
        bson.D{{"$match", bson.D{{"active", true}}}},
    }
    
    cursor, err := r.client.Database("admin").Aggregate(ctx, pipeline)
    if err != nil {
        return nil, err
    }
    defer cursor.Close(ctx)
    
    var ops []bson.M
    return ops, cursor.All(ctx, &ops)
}
```

Connection pooling should be configured at client creation with **retryable writes enabled**:

```go
clientOptions := options.Client().
    ApplyURI(uri).
    SetMaxPoolSize(100).
    SetMinPoolSize(10).
    SetMaxConnIdleTime(30 * time.Second).
    SetConnectTimeout(10 * time.Second).
    SetRetryWrites(true).
    SetRetryReads(true)
```

## SQLite stores backup history without CGO dependencies

For simpler deployment, use **modernc.org/sqlite** (pure Go, no CGO required). The performance difference versus mattn/go-sqlite3 is **10-50% slower on inserts** but acceptable for backup tracking workloads.

Critical configuration for concurrent access:

```go
func OpenDB(path string) (*sql.DB, error) {
    dsn := path + "?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_foreign_keys=true"
    db, err := sql.Open("sqlite", dsn)
    if err != nil {
        return nil, err
    }
    
    // SQLite works best with limited connections
    db.SetMaxOpenConns(1)  // Serializes writes
    db.SetMaxIdleConns(1)
    
    return db, nil
}
```

**WAL mode** (Write-Ahead Logging) enables readers to not block writers, essential for a dashboard querying backup status while jobs update records.

The repository pattern for backup history:

```go
type BackupRepository struct {
    db *sql.DB
}

func (r *BackupRepository) Create(ctx context.Context, databaseID int64) (*domain.BackupRecord, error) {
    query := `INSERT INTO backup_history (database_id, started_at, status) VALUES (?, ?, ?)`
    result, err := r.db.ExecContext(ctx, query, databaseID, time.Now().UTC(), "pending")
    if err != nil {
        return nil, err
    }
    id, _ := result.LastInsertId()
    return r.GetByID(ctx, id)
}

func (r *BackupRepository) UpdateStatus(ctx context.Context, id int64, status string, errMsg string) error {
    query := `UPDATE backup_history SET status = ?, error_message = ?, 
              completed_at = CASE WHEN ? IN ('completed', 'failed') THEN ? ELSE NULL END
              WHERE id = ?`
    now := time.Now().UTC()
    _, err := r.db.ExecContext(ctx, query, status, sql.NullString{String: errMsg, Valid: errMsg != ""}, 
        status, now, id)
    return err
}
```

## Scheduled backups use robfig/cron with graceful shutdown

The **robfig/cron/v3** library provides production-grade scheduling with cron expressions, timezone support, and panic recovery:

```go
type Scheduler struct {
    cron       *cron.Cron
    jobs       map[int64]cron.EntryID
    mu         sync.RWMutex
    executor   BackupExecutor
    ctx        context.Context
    cancel     context.CancelFunc
    wg         sync.WaitGroup
}

func NewScheduler(executor BackupExecutor) *Scheduler {
    ctx, cancel := context.WithCancel(context.Background())
    
    return &Scheduler{
        cron: cron.New(
            cron.WithChain(
                cron.Recover(cron.DefaultLogger),           // Recover from panics
                cron.SkipIfStillRunning(cron.DefaultLogger), // Prevent overlapping
            ),
            cron.WithLocation(time.UTC),
        ),
        jobs:     make(map[int64]cron.EntryID),
        executor: executor,
        ctx:      ctx,
        cancel:   cancel,
    }
}

func (s *Scheduler) AddJob(job *domain.BackupJob) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    entryID, err := s.cron.AddFunc(job.CronExpr, func() {
        s.runBackup(job)
    })
    if err != nil {
        return err
    }
    s.jobs[job.ID] = entryID
    return nil
}

func (s *Scheduler) runBackup(job *domain.BackupJob) {
    s.wg.Add(1)
    defer s.wg.Done()
    
    ctx, cancel := context.WithTimeout(s.ctx, 60*time.Minute)
    defer cancel()
    
    if err := s.executor.Execute(ctx, job.DatabaseID); err != nil {
        log.Printf("Backup job %d failed: %v", job.ID, err)
    }
}

func (s *Scheduler) Stop() {
    cronCtx := s.cron.Stop()  // Stop accepting new jobs
    s.cancel()                 // Signal running jobs to stop
    <-cronCtx.Done()           // Wait for cron to stop
    
    // Wait for running jobs with timeout
    done := make(chan struct{})
    go func() { s.wg.Wait(); close(done) }()
    
    select {
    case <-done:
        log.Println("Scheduler stopped gracefully")
    case <-time.After(30 * time.Second):
        log.Println("Scheduler stopped with timeout")
    }
}
```

## mongodump execution requires security-first command building

**Critical security rule**: Never use shell invocation (`sh -c`) with dynamic content. Always pass arguments as separate parameters:

```go
// ❌ VULNERABLE - shell injection possible
cmd := exec.Command("sh", "-c", "mongodump --db " + userInput)

// ✅ SAFE - arguments are parameterized
cmd := exec.Command("mongodump", "--db", dbName, "--out", outputPath)
```

Complete safe executor with timeout and streaming output:

```go
type BackupExecutor struct {
    config *BackupConfig
}

func (e *BackupExecutor) Execute(ctx context.Context, onProgress func(string)) (*BackupResult, error) {
    // Validate database name (alphanumeric + underscore only)
    if !regexp.MustCompile(`^[a-zA-Z0-9_]+$`).MatchString(e.config.Database) {
        return nil, fmt.Errorf("invalid database name")
    }
    
    // Build arguments (parameterized - safe from injection)
    args := []string{
        "--host", e.config.Host,
        "--port", fmt.Sprintf("%d", e.config.Port),
        "--db", e.config.Database,
        "--out", e.config.OutputPath,
        "--gzip",
    }
    
    if e.config.Username != "" {
        args = append(args, "--username", e.config.Username)
        args = append(args, "--authenticationDatabase", e.config.AuthDB)
    }
    
    // Create command with context for timeout
    execCtx, cancel := context.WithTimeout(ctx, 60*time.Minute)
    defer cancel()
    
    cmd := exec.CommandContext(execCtx, "mongodump", args...)
    
    // Set process group for proper cleanup on timeout
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    
    // Stream output in real-time
    stdout, _ := cmd.StdoutPipe()
    stderr, _ := cmd.StderrPipe()
    
    if err := cmd.Start(); err != nil {
        return nil, err
    }
    
    var wg sync.WaitGroup
    wg.Add(2)
    
    streamOutput := func(reader io.Reader) {
        defer wg.Done()
        scanner := bufio.NewScanner(reader)
        for scanner.Scan() {
            if onProgress != nil {
                onProgress(scanner.Text())
            }
        }
    }
    
    go streamOutput(stdout)
    go streamOutput(stderr)
    wg.Wait()
    
    if err := cmd.Wait(); err != nil {
        if execCtx.Err() == context.DeadlineExceeded {
            syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
            return nil, fmt.Errorf("backup timed out")
        }
        return nil, err
    }
    
    return &BackupResult{FilePath: e.config.OutputPath}, nil
}
```

## WebSocket implementation with coder/websocket outperforms gorilla

For new projects, **coder/websocket** (formerly nhooyr/websocket) is recommended over gorilla/websocket due to first-class context support, native concurrent writes, and modern API design.

Hub pattern for broadcasting metrics to all connected clients:

```go
type Hub struct {
    clients    map[*Client]bool
    register   chan *Client
    unregister chan *Client
    broadcast  chan []byte
    mu         sync.RWMutex
}

func (h *Hub) Run(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            h.shutdown()
            return
        case client := <-h.register:
            h.mu.Lock()
            h.clients[client] = true
            h.mu.Unlock()
        case client := <-h.unregister:
            h.mu.Lock()
            if _, ok := h.clients[client]; ok {
                delete(h.clients, client)
                close(client.send)
            }
            h.mu.Unlock()
        case message := <-h.broadcast:
            h.mu.RLock()
            for client := range h.clients {
                select {
                case client.send <- message:
                default:
                    close(client.send)
                    delete(h.clients, client)
                }
            }
            h.mu.RUnlock()
        }
    }
}

func (h *Hub) BroadcastMetrics(metrics *domain.Metrics) error {
    msg := Message{
        Type:      "metrics",
        Payload:   metrics,
        Timestamp: time.Now().UTC(),
    }
    data, err := json.Marshal(msg)
    if err != nil {
        return err
    }
    h.broadcast <- data
    return nil
}
```

WebSocket handler with authentication:

```go
func (h *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
        OriginPatterns:  []string{"*"},  // Configure for production
        CompressionMode: websocket.CompressionContextTakeover,
    })
    if err != nil {
        return
    }
    
    client := &Client{
        ID:   uuid.New().String(),
        hub:  h.hub,
        conn: conn,
        send: make(chan []byte, 256),
    }
    
    h.hub.register <- client
    
    go client.writePump()
    go client.readPump()
}

func (c *Client) writePump() {
    ticker := time.NewTicker(30 * time.Second)  // Ping interval
    defer ticker.Stop()
    
    for {
        select {
        case message, ok := <-c.send:
            if !ok {
                return
            }
            ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            err := c.conn.Write(ctx, websocket.MessageText, message)
            cancel()
            if err != nil {
                return
            }
        case <-ticker.C:
            ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            err := c.conn.Ping(ctx)
            cancel()
            if err != nil {
                return
            }
        }
    }
}
```

## React integration uses custom hooks with reconnection logic

TypeScript WebSocket hook with exponential backoff reconnection:

```typescript
export function useWebSocket<T>(url: string, options: WebSocketOptions = {}) {
  const [lastMessage, setLastMessage] = useState<T | null>(null);
  const [readyState, setReadyState] = useState<ReadyState>('CONNECTING');
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectCount = useRef(0);

  const connect = useCallback(() => {
    const ws = new WebSocket(url);
    wsRef.current = ws;

    ws.onopen = () => {
      setReadyState('OPEN');
      reconnectCount.current = 0;
    };

    ws.onclose = () => {
      setReadyState('CLOSED');
      if (options.shouldReconnect && reconnectCount.current < 10) {
        const delay = Math.min(1000 * Math.pow(2, reconnectCount.current), 30000);
        reconnectCount.current++;
        setTimeout(connect, delay);
      }
    };

    ws.onmessage = (event) => {
      try {
        setLastMessage(JSON.parse(event.data));
      } catch {}
    };
  }, [url, options.shouldReconnect]);

  useEffect(() => {
    connect();
    return () => wsRef.current?.close();
  }, [connect]);

  const sendMessage = useCallback((message: object) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify(message));
    }
  }, []);

  return { lastMessage, readyState, sendMessage };
}
```

Dashboard component using `useReducer` for efficient state management:

```typescript
function dashboardReducer(state: DashboardState, action: DashboardAction): DashboardState {
  switch (action.type) {
    case 'UPDATE_METRICS':
      return { ...state, metrics: action.payload, lastUpdate: new Date() };
    case 'UPDATE_BACKUP':
      const backups = new Map(state.backups);
      backups.set(action.payload.id, action.payload);
      return { ...state, backups };
    default:
      return state;
  }
}

export function MetricsDashboard() {
  const [state, dispatch] = useReducer(dashboardReducer, initialState);

  const { lastMessage, readyState } = useWebSocket<WebSocketMessage>(
    `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/ws`,
    { shouldReconnect: true }
  );

  useEffect(() => {
    if (!lastMessage) return;
    
    if (lastMessage.type === 'metrics') {
      dispatch({ type: 'UPDATE_METRICS', payload: lastMessage.payload });
    } else if (lastMessage.type === 'backup_status') {
      dispatch({ type: 'UPDATE_BACKUP', payload: lastMessage.payload });
    }
  }, [lastMessage]);

  return (
    <div>
      <ConnectionStatus connected={readyState === 'OPEN'} />
      {state.metrics && <MetricsPanel metrics={state.metrics} />}
      <BackupsPanel backups={Array.from(state.backups.values())} />
    </div>
  );
}
```

Use `React.memo()` for frequently updating metric components to prevent unnecessary re-renders.

## Conclusion

Building a production MongoDB management dashboard requires integrating multiple Go patterns and libraries cohesively. The **Clean Architecture** approach with manual dependency injection provides maximum clarity while remaining testable. MongoDB monitoring via `serverStatus` and `$currentOp` delivers comprehensive metrics, while **modernc.org/sqlite** offers CGO-free backup history storage. The **robfig/cron** library handles scheduled backups with proper lifecycle management, and **coder/websocket** combined with React hooks enables real-time metric streaming with automatic reconnection.

Key architectural decisions that will scale: define interfaces at consumer level, use context.Context for all operations with timeouts, implement graceful shutdown patterns throughout, and never invoke shells with dynamic content. These patterns establish a foundation that handles the complexity of coordinating MongoDB health checks, scheduled backup jobs, and live WebSocket updates in a single cohesive application.
