# Notes

## Email Service (Login Codes)

Use **Resend** for sending transactional emails (login/auth tokens).

- Free tier: 3,000 emails/month, 100/day
- Go SDK: `github.com/resendlabs/resend-go`
- Simple REST API, good deliverability, easy domain verification (DNS records)

Alternatives:
| Service | Free tier | Notes |
|---------|-----------|-------|
| Brevo | 300/day, 9k/month | Higher free volume |
| Postmark | 100/month | Best deliverability, very low free tier |
| AWS SES | 62k/month | Cheapest at scale, more setup |

## Deploying the Server

### Option 1 — Freebox Ultra (self-hosted, free)
- Built-in hypervisor (QEMU/KVM) at `mafreebox.freebox.fr` → VMs
- Create Ubuntu Server 24.04 VM, assign M.2 SSD storage
- Run binary as a systemd service
- Port forward 443 → VM:8080 in Freebox settings
- Use DuckDNS or similar for dynamic IP
- **Pros**: free, full control, SQLite file on local disk
- **Cons**: home power/internet dependency, no SLA

### Option 2 — Hetzner VPS (~€3.29/month, CAX11 ARM)
- 2 vCPU ARM, 4GB RAM, 40GB disk — way more than needed
- Run binary with systemd, SQLite on disk
- **Pros**: cheapest always-on paid option, fast, reliable
- **Cons**: manual server management

### Option 3 — Oracle Cloud Free Tier (free forever)
- 2 ARM VMs (1 OCPU, 1GB RAM each), permanently free
- Run binary with systemd
- **Pros**: actually free, reliable datacenter
- **Cons**: complex UI, requires credit card to sign up

### Database Notes
- SQLite with WAL mode + NORMAL synchronous (already configured at startup)
- DB path from `DB_PATH` env var (defaults to `app_limits.db` locally)
- Can migrate to PostgreSQL later — only the driver and `?` → `$1` placeholders need to change
