# Timox Server

Go backend for the Timox parental time management system. Provides a REST API for the child Flutter app and a server-rendered web dashboard for parents.

## Features

- JWT-based auth (email → one-time token → JWT)
- Parent/child role separation
- App usage limit management
- App icon upload with admin review queue
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
- Routes: `/users`, `/auth/*`, `/me`, `/app_limits`, `/children/*`, `/report`, `/icons/*`, `/apps/*`

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
| `icons.go` | App icon check, upload, and lookup handlers |
| `migrations.go` + `migrations/` | SQL migration system |
| `monitoring/` | Grafana + Loki + Promtail stack config and install scripts |
| `cmd/admin/` | Local admin tool for icon review (see [Admin Tool](#admin-tool)) |

### Database

SQLite with tables: `users`, `app_limits`, `auth_tokens`, `parent_children`, `app_usage`, `apps`, `pending_icons`. Tests use an in-memory DB (`:memory:`).

## Deployment

Targets a Linux host (Alpine/OpenRC) reachable via SSH as `freebox`. The binary is cross-compiled locally and copied over.

### Prerequisites

- SSH alias `freebox` configured in `~/.ssh/config`
- Remote directory `/opt/timox` exists and is writable
- Environment file at `/opt/timox/.env` (see `.env.example`):
  ```
  JWT_SECRET=your-strong-secret-here
  RESEND_API_KEY=re_...
  ICONS_DIR=/opt/timox/data
  ```
- OpenRC service installed on the remote:
  ```sh
  scp timox.openrc freebox:/etc/init.d/timox
  ssh freebox "chmod +x /etc/init.d/timox && doas rc-update add timox default"
  ```

### Caddy

Caddy acts as a reverse proxy in front of the Go server and also serves app icon files directly.

**Install on Alpine:**
```sh
ssh freebox
doas apk add caddy
doas rc-update add caddy default
doas rc-service caddy start
```

**Caddyfile** (`/etc/caddy/Caddyfile`):
```
{
  admin off
}

drp.freeboxos.fr {
    # Serve icon files directly — strip /static prefix so
    # /static/icons/com.example.app.png → /opt/timox/data/icons/com.example.app.png
    handle_path /static/* {
        root * /opt/timox/data
        file_server
    }

    # Everything else proxied to the Go server
    reverse_proxy localhost:8080
}
```

The `handle_path` directive strips the `/static` prefix before resolving the file, so `ICONS_DIR` on the Go server must match the `root` path set here (`/opt/timox/data`).

After editing the Caddyfile:
```sh
doas caddy fmt --overwrite /etc/caddy/Caddyfile
doas rc-service caddy reload
```

Make sure the icon directories exist and are readable by the `caddy` user:
```sh
doas mkdir -p /opt/timox/data/icons /opt/timox/data/pending-icons
doas chown -R caddy:caddy /opt/timox/data
```

The Go server process also writes to `/opt/timox/data`, so both the `timox` and `caddy` users need access. Add the `timox` user to the `caddy` group, then set group ownership and permissions:
```sh
doas adduser timox caddy
doas chown -R timox:caddy /opt/timox/data
doas chmod -R 775 /opt/timox/data
```

Verify both users are in the same group before restarting services:
```sh
grep caddy /etc/group   # should list both caddy and timox
```

### Deploy

```sh
./deploy.sh
```

This builds a static `linux/arm64` binary, copies it to `freebox:/opt/timox/timox-server`, and restarts the `timox` service via OpenRC. Logs are written to `/var/log/timox/`.

## Admin Tool

`cmd/admin` is a local-only HTTP server for reviewing pending app icons. It connects directly to the SQLite DB and filesystem — no auth, no JWT. Access is controlled entirely by the SSH tunnel.

### Usage

**Run locally** (dev, uses local DB and `./data`):

```sh
make admin
```

**Run on the server** (builds for `linux/arm64`, deploys, opens SSH tunnel, cleans up on exit):

```sh
make admin-remote
```

`admin-remote` compiles the binary, copies it to `freebox:/opt/timox/timox-admin`, starts it there, and opens an SSH tunnel so `http://localhost:9191` works in your browser. When you hit Ctrl+C the remote process is killed and the local binary is deleted.

Then open `http://localhost:9191` in your browser. The UI shows all pending icons as a card grid with **Approve** and **Reject** buttons.

- **Approve** — moves the file from `pending-icons/` to `icons/`, sets `icon_path` in the `apps` table, removes the `pending_icons` record.
- **Reject** — deletes the file and removes the `pending_icons` record.

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



## TODOs

- Verify child timezone: the server uses UTC to determine the current date, but the child device reports cumulative usage since its local midnight. If the device is in a timezone behind UTC, high cumulative values from the end of the previous local day get written under the new server date. The current mitigation is last-reported-wins (no MAX) so the device's reset at local midnight corrects the inflated value. A proper fix would be to accept the device's local date in the `/report` payload and store usage under that date instead.
