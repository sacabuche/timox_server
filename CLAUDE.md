# Code Guidelines

## Style

- Prefer early returns over nested `if` blocks.

## Deployment

- The server runs on a Freebox home server (Alpine Linux), accessible via the SSH alias `freebox`.
- `make admin-remote` builds the admin tool for `linux/arm64`, deploys it to `freebox:/opt/timox/timox-admin`, opens an SSH tunnel on port 9191, and cleans up on exit.
- Alpine uses OpenRC (no sudo, no systemd). To restart a service: `su -c "rc-service <name> restart"`.

## Static Files / Icons

- In production, Caddy serves `/static/*` directly from `/opt/timox/data`, so the Go server never handles those requests.
- In development, set `SERVE_STATIC=true` in `.env` to have the Go server serve `/static/*` from `ICONS_DIR` (default `./data`).
- Icon paths stored in the DB are relative to `ICONS_DIR`, e.g. `icons/com.example.app.png`. The web UI prefixes them with `/static/` to form the URL.
- Pending icons are stored in `$ICONS_DIR/pending-icons/` as `{userUUID}-{packageName}.png`. On approval they are moved to `$ICONS_DIR/icons/{packageName}.png`.
