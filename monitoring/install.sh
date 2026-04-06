#!/bin/sh
set -e

REMOTE=freebox
REMOTE_DIR=/opt/monitoring
PROMTAIL_VERSION=3.4.1

echo "==> Creating remote directory..."
ssh "$REMOTE" "doas mkdir -p $REMOTE_DIR && doas chown daniel:daniel $REMOTE_DIR"

echo "==> Copying config files..."
scp docker-compose.yml loki-config.yml grafana-datasources.yml promtail-config.yml "$REMOTE:$REMOTE_DIR/"

echo "==> Copying Promtail OpenRC script..."
scp promtail.openrc "$REMOTE:/tmp/promtail"
ssh "$REMOTE" "doas cp /tmp/promtail /etc/init.d/promtail && doas chmod +x /etc/init.d/promtail"

echo "==> Downloading Promtail binary on remote..."
ssh "$REMOTE" "
  cd $REMOTE_DIR
  if [ ! -f promtail-linux-arm64 ]; then
    wget -q https://github.com/grafana/loki/releases/download/v${PROMTAIL_VERSION}/promtail-linux-arm64.zip
    unzip -o promtail-linux-arm64.zip
    chmod +x promtail-linux-arm64
    rm promtail-linux-arm64.zip
  fi
"

echo ""
echo "==> Next steps on freebox:"
echo "  1. Create /opt/monitoring/.env with: GRAFANA_PASSWORD=yourpassword"
echo "  2. Run: cd /opt/monitoring && doas docker compose up -d"
echo "  3. Run: doas rc-update add promtail && doas rc-service promtail start"
echo ""
echo "  Grafana will be at http://freebox:3000 (admin / your password)"
