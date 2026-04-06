#!/bin/sh
set -e

REMOTE=freebox

echo "==> Installing logrotate..."
ssh "$REMOTE" "doas apk add --no-cache logrotate"

echo "==> Writing logrotate config..."
ssh "$REMOTE" "doas tee /etc/logrotate.d/timox > /dev/null <<'EOF'
/var/log/timox/*.log {
    daily
    rotate 3
    compress
    missingok
    notifempty
    copytruncate
}
EOF"

echo "==> Enabling and starting crond..."
ssh "$REMOTE" "doas rc-update add crond default && doas rc-service crond start || true"

echo "==> Testing logrotate config..."
ssh "$REMOTE" "doas logrotate --debug /etc/logrotate.d/timox"

echo "Done."
