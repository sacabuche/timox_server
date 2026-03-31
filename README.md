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
