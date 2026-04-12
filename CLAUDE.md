# Code Guidelines

## Style

- Prefer early returns over nested `if` blocks.

## Deployment

- The server runs on a Freebox home server (Alpine Linux), accessible via the SSH alias `freebox`.
- `make admin-remote` builds the admin tool for `linux/arm64`, deploys it to `freebox:/opt/timox/timox-admin`, opens an SSH tunnel on port 9191, and cleans up on exit.
- Alpine uses OpenRC (no sudo, no systemd). Use `doas` for privileged commands. To restart a service: `doas rc-service <name> restart`.
- The `Caddyfile` in the repo root is the production Caddy config. Deploy it with: `scp Caddyfile freebox:/etc/caddy/Caddyfile && ssh freebox "doas rc-service caddy restart"`. (Use `restart`, not `reload` — admin API is disabled.)

## Admin Server (`cmd/admin`)

The admin server runs on `localhost:9191` (accessed via SSH tunnel with `make admin-remote`). It has two screens:

### Icon Review (`GET /`)
Lists pending app icons submitted by children and waiting for manual approval.
- `POST /approve/{id}` — moves the file from `$ICONS_DIR/pending-icons/` to `$ICONS_DIR/icons/`, updates `apps.icon_path`, deletes the `pending_icons` row.
- `POST /reject/{id}` — deletes the file and the `pending_icons` row.
- `GET /img/{filename}` — serves pending icon images from `$ICONS_DIR/pending-icons/`.

### Zero-Time Apps (`GET /zero-apps`)
Lists apps that appear in `app_usage` but whose `SUM(total_used_minutes) = 0` across all records (apps reported by a device but never actually used). Shows the app icon if one exists.
- `POST /zero-apps/delete` — deletes a single app (form field: `package_name`) from all tables: `app_usage`, `app_limits`, `pending_app_limits`, `app_schedules`, `pending_icons`, `apps`. Refuses if the app has any non-zero usage (safety check).
- `POST /zero-apps/delete-all-no-icon` — bulk-deletes all zero-time apps that also have no icon, in a single transaction.
- `GET /icon/{filename}` — serves approved icon images from `$ICONS_DIR/icons/`.

Both screens share a nav bar for switching between them.

## Static Files / Icons

- In production, Caddy serves `/static/*` directly from `/opt/timox/data`, so the Go server never handles those requests.
- In development, set `SERVE_STATIC=true` in `.env` to have the Go server serve `/static/*` from `ICONS_DIR` (default `./data`).
- Icon paths stored in the DB are relative to `ICONS_DIR`, e.g. `icons/com.example.app.png`. The web UI prefixes them with `/static/` to form the URL.
- Pending icons are stored in `$ICONS_DIR/pending-icons/` as `{userUUID}-{packageName}.png`. On approval they are moved to `$ICONS_DIR/icons/{packageName}.png`.
