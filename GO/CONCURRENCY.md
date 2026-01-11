# Production Go backend patterns from Uber, Google, and Cloudflare

Modern Go backend development in 2025 centers on a core principle that top engineering teams have converged upon: **explicit, bounded concurrency with strong cancellation propagation**. Companies like Uber, Google, and Cloudflare have battle-tested patterns that prioritize preventing resource leaks over raw performance—using structured concurrency libraries like Sourcegraph's `conc`, bounded worker pools via `errgroup.SetLimit()`, and always answering "when will this goroutine exit?" Uber's internal analysis found **~2,000 data races in six months**, driving their investment in explicit lifecycle management. The patterns detailed below represent hard-won production wisdom from services handling millions of requests per second.

## Structured concurrency has replaced raw goroutines at scale

The old pattern of `go func() {...}()` scattered throughout production code has fallen out of favor at high-scale companies. The problem isn't performance—goroutines are cheap—but lifecycle management. Uber's style guide mandates that **every goroutine must have an owner responsible for collecting it**.

The **`golang.org/x/sync/errgroup`** package has become the de facto standard for bounded concurrent operations. Its `SetLimit()` method (added in Go 1.21) enables straightforward worker pool semantics without external dependencies:

```go
func ProcessItems(ctx context.Context, items []Item) error {
    g, ctx := errgroup.WithContext(ctx)
    g.SetLimit(10) // Max 10 concurrent goroutines
    
    for _, item := range items {
        item := item
        g.Go(func() error {
            return processItem(ctx, item)
        })
    }
    return g.Wait() // Returns first error, cancels others via ctx
}
```

For more complex workflows, **Sourcegraph's `conc` library** (10.2k GitHub stars) provides panic propagation, result collection, and iterator patterns. Their key insight: all goroutines should have owners that must collect them, preventing leaks by design. Uber's `cff` package takes this further with DAG-based task dependencies and code generation.

**When to use worker pools versus direct goroutines**: Use pools when limiting resource-intensive operations (database connections, HTTP requests, file I/O) or when unbounded growth could cause memory pressure. Spawn goroutines directly only for few, short-lived tasks where memory isn't a concern.

## Database connection pooling requires careful tuning beyond defaults

The **`pgx` driver** has definitively replaced `lib/pq` for PostgreSQL—the latter's maintainers explicitly recommend migration. Beyond native binary protocol performance (~3x faster JSON marshalling), `pgx` offers critical features: automatic statement caching, `pgxpool` with jitter support to prevent thundering herd reconnections, and better connection validation hooks.

Production-grade pool configuration involves more than setting sizes:

```go
config.MaxConns = 25                           // Not too high—contention at DB
config.MinConns = 5                            // Warm connections reduce tail latency
config.MaxConnLifetime = time.Hour             // Prevent stale connections
config.MaxConnLifetimeJitter = 15 * time.Minute // Prevent all connections expiring together
```

The **jitter parameter is critical** but often overlooked—without it, all connections may expire simultaneously during traffic spikes, causing latency spikes as the pool rebuilds. Formula for sizing: `(Available DB Connections × 0.8) / Number of App Instances`.

For Redis, the landscape has shifted. While `go-redis` remains the most popular choice, **`rueidis` offers ~14x throughput** through automatic pipelining and RESP3 client-side caching. New projects with extreme Redis performance requirements should evaluate `rueidis`; existing `go-redis` codebases rarely benefit from migration.

## HTTP client misconfiguration causes silent connection thrashing

The most common production mistake is leaving `http.Transport.MaxIdleConnsPerHost` at its **default of 2**—this causes constant connection churn under load. Production HTTP clients require explicit configuration:

```go
transport := &http.Transport{
    MaxIdleConnsPerHost: 100,          // Critical: default is only 2
    MaxConnsPerHost:     100,          // Active connection limit (Go 1.11+)
    IdleConnTimeout:     90 * time.Second,
    ResponseHeaderTimeout: 30 * time.Second,
}
```

**gRPC connection pooling** at scale requires understanding HTTP/2 multiplexing: a single connection handles many concurrent RPCs, but becomes a bottleneck above ~100 concurrent streams. High-throughput services use connection pools (multiple underlying TCP connections) with round-robin selection. The `grpc-go` keepalive settings must coordinate between client and server—mismatches cause "transport is closing" errors.

## Context cancellation propagates deadlines across service boundaries

Google's internal mandate—**pass context as the first argument to every function on the call path**—has become industry standard. The pattern enables automatic request cancellation when clients disconnect and deadline propagation across microservice calls.

The critical timeout strategy from Grab's engineering blog: **upstream timeouts must exceed downstream timeouts plus retry time**. When Service A calls Service B which calls Service C, if C's P99 latency is 600ms and B adds 100ms processing, Service A's context timeout must be at least 750ms. Otherwise, B continues processing after A has given up—wasting resources.

Graceful shutdown in Kubernetes environments requires **`signal.NotifyContext`** (Go 1.16+) combined with readiness probe coordination:

```go
ctx, stop := signal.NotifyContext(context.Background(), 
    syscall.SIGTERM, syscall.SIGINT)
defer stop()

// Mark unhealthy immediately on signal
isShuttingDown.Store(true)
time.Sleep(5 * time.Second)  // Allow probe propagation
srv.Shutdown(ctx)
```

**Context values should inform, not control**—store trace IDs, user IDs, and request metadata, but never business logic parameters, database connections, or configuration. Type-safe unexported keys prevent collisions across packages.

## Production profiling requires security-conscious pprof deployment

A **2024 security analysis found 296,000+ vulnerable pprof endpoints** exposed to the internet (CVE-2019-11248). Production pprof endpoints must run on separate localhost ports, require authentication, and use rate limiting. Never expose `/debug/pprof/` on your main application port.

**Continuous profiling** has matured significantly. Grafana Pyroscope (acquired from standalone Pyroscope) and Parca provide production-grade profiling with <1% overhead through eBPF-based collection. Datadog's continuous profiler includes Profile-Guided Optimization (PGO) integration—Uber reports **12%+ performance improvements** from continuous PGO pipelines.

The Go execution tracer received major improvements in **Go 1.22, reducing overhead from 10-20% to 1-2%**. Go 1.25 introduces flight recording—capturing traces retroactively after issues occur rather than requiring always-on collection.

For flame graph analysis: width represents cumulative time (not chronological progression), wide bars indicate hot spots, and `runtime.gopark` bars reveal scheduler overhead. The `gotraceui` tool from Dominik Honnef offers faster analysis than `go tool trace` without browser requirements.

## Error handling patterns have standardized around wrapping

The `pkg/errors` library is **in maintenance mode**—Go 1.13+ standard library adopted its `Is`, `As`, and `Unwrap` patterns. New code should use `fmt.Errorf("context: %w", err)` for wrapping and `errors.Is/As` for checking. The only feature pkg/errors provided that the standard library doesn't: stack traces with `%+v` formatting.

Uber's error handling guidance distinguishes when to use each pattern:

| Approach | Use Case |
|----------|----------|
| Sentinel errors (`var ErrNotFound = ...`) | Package-level known conditions |
| Custom error types | When additional context fields needed |
| `%w` wrapping | Adding context while preserving cause |
| `%v` (not `%w`) | Hiding internals at system boundaries |

**Go 1.20's `errors.Join`** enables multi-error aggregation where all wrapped errors remain accessible via `Is/As`—useful for validation that collects multiple failures.

## Structured logging converges on slog for new projects

The **`log/slog`** package (Go 1.21) has changed the calculus for logging library selection. For new projects without extreme performance requirements, slog provides zero-dependency structured logging with 40 bytes per allocation—matching zerolog's efficiency.

Benchmarks show **zerolog fastest overall, zap close behind with more features**, and slog competitive for most workloads. The recommendation matrix:
- **New Go 1.21+ projects**: Start with slog
- **High-performance requirements**: zerolog or zap
- **Existing projects**: Migration rarely worth the churn

Google's logging guidance emphasizes that **ERROR level should be actionable**—not just "more serious" than warning. If no action is required, it's not an error.

OpenTelemetry has become the standard for distributed tracing integration. The pattern involves initializing a TracerProvider with OTLP export, wrapping HTTP handlers with `otelhttp.NewHandler`, and propagating context through all downstream calls.

## Testing concurrent code requires deliberate patterns

Running **`go test -race`** is non-negotiable—Uber found thousands of races by making this mandatory. The overhead (5-10x memory, 2-20x execution time) is acceptable for CI pipelines.

Table-driven tests with `t.Parallel()` require capturing the loop variable:

```go
for _, tt := range tests {
    tt := tt // CRITICAL before Go 1.22
    t.Run(tt.name, func(t *testing.T) {
        t.Parallel()
        // test using tt
    })
}
```

**`testcontainers-go`** has become the standard for integration testing, used by Elastic, Intel, and the OpenTelemetry project. It spins up real PostgreSQL, Redis, or Kafka containers with automatic cleanup:

```go
pgContainer, _ := postgres.Run(ctx, "postgres:16",
    postgres.WithDatabase("testdb"))
defer pgContainer.Terminate(ctx)
connStr, _ := pgContainer.ConnectionString(ctx)
```

Uber's `goleak` package detects goroutine leaks in tests—essential for catching the resource leaks that production code at scale must avoid.

## Architectural patterns emphasize domain isolation

The Three Dots Labs approach—combining DDD Lite, CQRS, and Clean Architecture—has gained significant traction for complex Go services. The key insight: **Go's implicit interface satisfaction enables clean port definitions without coupling**.

The layer structure isolates concerns:
- **Domain**: Business entities with behavior (not anemic models)
- **Application**: Use case orchestration
- **Ports**: Interface definitions (input/output boundaries)
- **Adapters**: Infrastructure implementations

Uber's `fx` framework handles dependency injection through function-based providers rather than struct tags, enabling compile-time verification of dependency graphs. For simpler services, constructor injection without a framework remains appropriate.

## Conclusion

Production Go in 2025 emphasizes **explicit over implicit**: bounded concurrency via `errgroup.SetLimit()` rather than unbounded goroutines, explicit connection pool configuration rather than driver defaults, structured context propagation rather than global state. The ecosystem has consolidated around fewer, better-maintained libraries—`pgx` for PostgreSQL, `slog` for logging, OpenTelemetry for observability.

The critical insight from companies at scale: **preventing resource leaks matters more than micro-optimization**. Uber's discovery of 2,000 data races demonstrates that even expert teams benefit from structured concurrency patterns and mandatory race detection. The patterns documented here represent defensive engineering—assuming that if a goroutine can leak, it eventually will.
