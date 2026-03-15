# CLAUDE.md — Go API Service

This file provides guidance for Claude when working in this repository. It covers project structure, conventions, and commands Claude should know to be effective.

---

## Project Overview

This is a Go service that exposes a REST (or gRPC) API. The service is structured for clarity, testability, and production readiness.

---

## Tech Stack

- **Language:** Go 1.22+
- **Router/Framework:** `gin-gonic/gin`
- **External API:** Double HQ (`api.doublehq.com`) — OAuth2 client credentials (client ID + client secret)
- **Database:** PostgreSQL via `pgx` or `database/sql` — update as needed
- **Config:** Environment variables loaded via `godotenv` or `viper`
- **Logging:** `slog` (stdlib) or `zerolog` / `zap`
- **Testing:** `testing` (stdlib) + `testify`
- **Build/Run:** `make` targets defined in `Makefile`

---

## Project Structure

```
.
├── cmd/
│   └── api/
│       └── main.go          # Entry point; wires dependencies and starts server
├── internal/
│   ├── handler/             # Gin handlers (controllers)
│   ├── service/             # Business logic layer
│   ├── repository/          # Data access layer (DB queries)
│   ├── middleware/          # Gin middleware (auth, logging, recovery)
│   ├── model/               # Domain structs (no DB or HTTP tags here)
│   └── config/              # Config loading and validation
├── pkg/
│   └── doublehq/            # Double HQ API client (see section below)
├── migrations/              # SQL migration files (up/down)
├── api/                     # OpenAPI/Swagger specs
├── .env.example             # Template for required environment variables
├── Makefile
├── go.mod
└── go.sum
```

### Layer Responsibilities

| Layer | Package | Responsibility |
|---|---|---|
| Entry point | `cmd/api` | Start server, wire dependencies |
| Handler | `internal/handler` | Parse requests, call service, write responses |
| Service | `internal/service` | Business logic, orchestration |
| Repository | `internal/repository` | All DB queries, no business logic |
| Model | `internal/model` | Pure Go structs, no framework coupling |
| Middleware | `internal/middleware` | Cross-cutting concerns |

---

## Common Commands

```bash
# Run the service locally
make run

# Build the binary
make build

# Run all tests
make test

# Run tests with race detector
make test-race

# Lint (requires golangci-lint)
make lint

# Apply DB migrations
make migrate-up

# Roll back last migration
make migrate-down

# Generate mocks (if using mockery or moq)
make generate
```

---

## Key Conventions

### Error Handling

- **Never ignore errors.** Always handle or explicitly propagate them.
- Return errors up the call stack; only handle them at the handler layer.
- Use `fmt.Errorf("context: %w", err)` for wrapping.
- Define sentinel errors in the service layer (e.g., `ErrNotFound`, `ErrUnauthorized`); map them to HTTP status codes in the handler.

```go
// service layer
var ErrNotFound = errors.New("resource not found")

// handler layer
if errors.Is(err, service.ErrNotFound) {
    http.Error(w, "not found", http.StatusNotFound)
    return
}
```

### Gin Handlers

- Handlers receive `*gin.Context` — use it for parsing, binding, and responding.
- Always use `c.ShouldBindJSON(&req)` for request bodies; handle the error before proceeding.
- Respond with `c.JSON(statusCode, payload)` — never mix `c.JSON` and `c.AbortWithStatus` in the same path.
- Use `c.AbortWithStatusJSON` inside middleware to short-circuit the chain.
- Group related routes with `r.Group("/api/v1/...")` and attach middleware at the group level.

```go
func (h *UserHandler) GetUser(c *gin.Context) {
    id := c.Param("id")
    user, err := h.svc.GetUser(c.Request.Context(), id)
    if err != nil {
        writeError(c, err)
        return
    }
    c.JSON(http.StatusOK, gin.H{"data": user})
}

// Router setup
v1 := r.Group("/api/v1")
v1.Use(middleware.Auth())
{
    v1.GET("/users/:id", userHandler.GetUser)
    v1.POST("/users", userHandler.CreateUser)
}
```

### Context Usage

- Always pass `context.Context` as the **first argument** in service and repository functions.
- Never store context in structs.
- Respect context cancellation in long-running DB calls and external requests.

### Configuration

- All config is loaded from environment variables at startup.
- Fail fast: if a required env var is missing, exit with a clear error message.
- Never hardcode secrets, ports, or DSNs in source code.

```go
type Config struct {
    Port     string
    DBDSN    string
    LogLevel string
}

func Load() (*Config, error) {
    // validate required fields here
}
```

### Dependency Injection

- Use constructor functions, not `init()` or global state.
- Pass dependencies explicitly; avoid package-level singletons.

```go
func NewUserHandler(svc UserService) *UserHandler {
    return &UserHandler{svc: svc}
}
```

### Logging

- Log at the handler/middleware level for request lifecycle.
- Log errors with context (e.g., `slog.Error("failed to get user", "id", id, "err", err)`).
- Do not log sensitive data (passwords, tokens, PII).

---

## Testing

- Unit test each layer in isolation using interfaces and mocks.
- Use `testify/assert` and `testify/require` for assertions.
- Integration tests live in `_test` packages and may use a real DB (set up via Docker Compose or testcontainers).
- Table-driven tests are preferred for handler and service logic.

```go
func TestGetUser(t *testing.T) {
    tests := []struct {
        name    string
        id      string
        want    *model.User
        wantErr error
    }{
        // cases here
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // ...
        })
    }
}
```

---

## API Design Guidelines

- Use consistent JSON response shapes:
  ```json
  { "data": { ... } }           // success
  { "error": "message here" }   // failure
  ```
- Version the API via URL prefix: `/api/v1/...`
- Use plural nouns for resource paths: `/users`, `/orders`
- Use standard HTTP verbs and status codes correctly.
- Document endpoints in `api/openapi.yaml` (or equivalent).

---

## Double HQ Integration

Double HQ is an external practice management platform. This service communicates with it via its REST API.

- **Base URL:** `https://api.doublehq.com`
- **Swagger docs:** `https://api.doublehq.com/api-docs`
- **Auth:** OAuth2 client credentials — authenticate with `DOUBLE_HQ_CLIENT_ID` and `DOUBLE_HQ_CLIENT_SECRET` to obtain a bearer token. Refresh the token before it expires; do not re-authenticate on every request.

### Client Structure

The Double HQ client lives in `pkg/doublehq/` and is consumed by the service layer only — never directly from handlers.

```
pkg/doublehq/
├── client.go       # HTTP client, token management, base request logic
├── client_test.go  # Unit tests using a mock HTTP server
└── models.go       # Request/response structs for Double HQ API
```

### Interface Pattern

Always define an interface in the service layer so the Double HQ client can be mocked in tests:

```go
// internal/service/user_service.go
type DoubleHQClient interface {
    GetContact(ctx context.Context, id string) (*doublehq.Contact, error)
    CreateContact(ctx context.Context, req doublehq.CreateContactRequest) (*doublehq.Contact, error)
}
```

### Required Environment Variables

```
DOUBLE_HQ_CLIENT_ID=...
DOUBLE_HQ_CLIENT_SECRET=...
DOUBLE_HQ_BASE_URL=https://api.doublehq.com   # override for local/staging
```

### Rules

- **Never** call the Double HQ API from a handler — always go through the service layer.
- **Always** pass `context.Context` into client methods so timeouts and cancellations propagate.
- **Cache** the access token in memory; wrap token refresh logic in the client itself (callers should not manage tokens).
- **Log** outbound request details (method, path, status) at `DEBUG` level; log errors at `ERROR` level.
- **Do not** retry automatically on failure — let the caller decide retry strategy.
- Set a **timeout** on all outbound requests (e.g., 10s default); make it configurable via env var.

---

## What Claude Should Prioritize

1. **Follow the layered architecture strictly** — no DB calls in handlers, no HTTP concerns in services.
2. **Keep functions small and single-purpose.**
3. **Write tests alongside new code**, not after.
4. **Prefer stdlib** unless a third-party package is already in use in the codebase.
5. **Ask before adding new dependencies** — justify the addition.
6. **Do not silently swallow errors** or use `_ = err`.
7. **Always handle context cancellation** in DB and HTTP client calls.
