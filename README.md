# Timox Server

Go backend for the Timox parental time management system. Provides a REST API for the child Flutter app and a server-rendered web dashboard for parents.

## Features

- JWT-based auth (email → one-time token → JWT)
- Parent/child role separation
- App usage limit management
- Web dashboard for parents
- SQLite storage (no CGO, uses `modernc.org/sqlite`)

## Getting Started

```bash
go run .          # Start server on :8080
go test ./...     # Run all tests
go build -o timox_server .  # Build binary
```

Set the `JWT_SECRET` environment variable in production (defaults to a dev secret).

## Architecture

Two interfaces on the same port:

**REST API** (consumed by child Flutter app)
- Auth: JWT Bearer tokens (HS256), role-based (`parent` / `child`)
- Routes: `/users`, `/auth/*`, `/me`, `/app_limits`, `/children/*`, `/report`

**Web UI** (`/web/*`)
- Server-rendered HTML dashboard for parents
- Cookie-based auth (`session_jwt`)

### Key Files

| File | Description |
|------|-------------|
| `main.go` | DB setup, JWT helpers, middleware, API handlers, router |
| `children.go` | Parent-children relationship handlers |
| `web.go` | `WebHandler` struct and web routes |
| `web_auth.go` | Web dashboard auth handlers |
| `web_children.go` | Web dashboard children management |
| `web_apps.go` | Web dashboard app limits |
| `migrations.go` + `migrations/` | SQL migration system |
| `monitoring/` | Grafana + Loki + Promtail stack config and install scripts |

### Database

SQLite with tables: `users`, `app_limits`, `auth_tokens`, `parent_children`, `app_usage`. Tests use an in-memory DB (`:memory:`).

## Deployment

Targets a Linux host (Alpine/OpenRC) reachable via SSH as `freebox`. The binary is cross-compiled locally and copied over.

### Prerequisites

- SSH alias `freebox` configured in `~/.ssh/config`
- Remote directory `/opt/timox` exists and is writable
- Environment file at `/opt/timox/.env` (see `.env.example`):
  ```
  JWT_SECRET=your-strong-secret-here
  RESEND_API_KEY=re_...
  ```
- OpenRC service installed on the remote:
  ```sh
  scp timox.openrc freebox:/etc/init.d/timox
  ssh freebox "chmod +x /etc/init.d/timox && doas rc-update add timox default"
  ```

### Deploy

```sh
./deploy.sh
```

This builds a static `linux/arm64` binary, copies it to `freebox:/opt/timox/timox-server`, and restarts the `timox` service via OpenRC. Logs are written to `/var/log/timox/`.

## Monitoring

Logs are shipped to **Loki** and visualized in **Grafana**. The stack runs on `freebox` via Docker Compose. Config files are in `monitoring/`.

### Architecture

```
/var/log/timox/stdout.log  ─┐
/var/log/timox/stderr.log  ─┴─ Promtail (OpenRC) → Loki:3100 → Grafana:3000
```

- **Promtail** — runs as an OpenRC service, tails `/var/log/timox/*.log`, ships to Loki
- **Loki** — stores logs, retains 30 days
- **Grafana** — UI at `http://freebox:3000` (user: `admin`)

### Logging

The app logs structured JSON to stderr via `slog`. Example:

```json
{"time":"2026-04-06T10:23:01Z","level":"INFO","msg":"request","method":"GET","path":"/app_limits","user":"abc-123"}
```

Use `slog` for all logging:

```go
slog.Info("something happened", "key", value)
slog.Error("db query failed", "err", err)
```

**User context in logs**

Authenticated API routes (Bearer JWT) automatically attach the user ID to every log line. This is done in `authMiddleware` — once the token is validated, a `*slog.Logger` with `"user"` pre-attached is stored in the request context. Handlers retrieve it with `logFromCtx(r.Context())`:

```go
logFromCtx(r.Context()).Info("something happened", "key", value)
```

Falls back to the global logger (no user field) for unauthenticated routes.

> **Note:** Web routes (`/web/*`) use cookie-based session auth, not `authMiddleware`, so they do not automatically carry user context in logs. Log the user manually there if needed.

**What is logged:**

| Event | Fields |
|-------|--------|
| Authenticated request | `method`, `path`, `user` |
| Parent login | `user`, `role` |
| Child login | `user`, `role` |
| App limits fetch | `user` |
| Limits version check | `user` |
| Usage report received | `user`, `count` |
| Per-app usage entry | `user`, `package`, `app`, `minutes` |

### Install

If Docker is not yet installed on `freebox`:

```sh
ssh freebox
doas apk add docker docker-cli-compose
doas rc-update add docker default
doas rc-service docker start
```

Then from your local machine:

```sh
cd monitoring
./install.sh              # deploys Loki + Grafana + Promtail to freebox
./install-logrotate.sh    # sets up log rotation on freebox
```

After running `install.sh`, on `freebox`:

```sh
# Install required system packages
doas apk add gcompat curl

# Create the env file
echo "GRAFANA_PASSWORD=yourpassword" > /opt/monitoring/.env

# Start the stack
cd /opt/monitoring && doas docker compose up -d

# Start Promtail
doas rc-update add promtail && doas rc-service promtail start
```

> **Note:** `gcompat` is required on Alpine because the Promtail binary is dynamically linked against glibc. Without it, the binary will fail with "not found" even though the file exists.

### Verifying the Stack

Check Promtail is running:

```sh
rc-service promtail status
tail -f /var/log/promtail/stderr.log
```

Check Loki is ready and receiving data:

```sh
curl -s http://localhost:3100/ready
curl -s http://localhost:3100/loki/api/v1/labels
```

The labels response should include `app` and `stream` once logs are flowing.

### Accessing Logs

**Live logs on the server:**

```sh
ssh freebox "tail -f /var/log/timox/stderr.log"
```

**Via Grafana UI:**

Open `http://freebox:3000` in your browser, log in as `admin`, then go to Explore → select Loki datasource.

### Querying in Grafana

Go to Explore → Loki datasource:

```logql
{app="timox"}                          # all logs
{app="timox", stream="stderr"}         # stderr only
{app="timox"} | json | level="ERROR"  # errors
{app="timox"} | json | user="abc-123" # logs for a specific user
```

### Log Rotation

Raw log files on disk are rotated daily, keeping 3 compressed days. Loki retains its own copy for 30 days independently.

