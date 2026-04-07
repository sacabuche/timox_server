admin:
	DB_PATH=app_limits.db ICONS_DIR=./data go run ./cmd/admin

admin-remote:
	GOOS=linux GOARCH=arm64 go build -o timox-admin ./cmd/admin
	scp timox-admin freebox:/opt/timox/timox-admin
	@REMOTE_PID=$$(ssh freebox 'DB_PATH=/opt/timox/data/app_limits.db ICONS_DIR=/opt/timox/data nohup /opt/timox/timox-admin > /tmp/timox-admin.log 2>&1 & echo $$!'); \
	trap "echo 'Stopping admin on freebox...'; ssh freebox kill $$REMOTE_PID 2>/dev/null; rm -f timox-admin" EXIT INT TERM; \
	echo "Tunnel open → http://localhost:9191 (Ctrl+C to stop)"; \
	ssh -L 9191:localhost:9191 -N freebox
