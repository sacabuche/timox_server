package main

import (
	"database/sql"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	db       *sql.DB
	iconsDir string
)

type PendingIcon struct {
	ID          string
	UserUUID    string
	PackageName string
	AppName     string
	SubmittedAt string
	FileName    string
}

func main() {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/data/app_limits.db"
	}
	iconsDir = os.Getenv("ICONS_DIR")
	if iconsDir == "" {
		iconsDir = "/data"
	}

	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.HandleFunc("GET /img/{filename}", handleServeIcon)
	mux.HandleFunc("POST /approve/{id}", handleApprove)
	mux.HandleFunc("POST /reject/{id}", handleReject)

	addr := "localhost:9191"
	log.Printf("admin listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func listPending() ([]PendingIcon, error) {
	rows, err := db.Query(`
		SELECT pi.id, pi.user_uuid, pi.package_name, COALESCE(a.app_name, ''), pi.submitted_at
		FROM pending_icons pi
		LEFT JOIN apps a ON a.package_name = pi.package_name
		ORDER BY pi.submitted_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var icons []PendingIcon
	for rows.Next() {
		var p PendingIcon
		if err := rows.Scan(&p.ID, &p.UserUUID, &p.PackageName, &p.AppName, &p.SubmittedAt); err != nil {
			return nil, err
		}
		p.FileName = p.UserUUID + "-" + sanitize(p.PackageName) + ".png"
		icons = append(icons, p)
	}
	return icons, nil
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	icons, err := listPending()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := indexTmpl.Execute(w, icons); err != nil {
		log.Printf("template error: %v", err)
	}
}

func handleServeIcon(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, filepath.Join(iconsDir, "pending-icons", filename))
}

func handleApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var userUUID, packageName string
	err := db.QueryRow("SELECT user_uuid, package_name FROM pending_icons WHERE id = ?", id).
		Scan(&userUUID, &packageName)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	src := filepath.Join(iconsDir, "pending-icons", userUUID+"-"+sanitize(packageName)+".png")
	dstDir := filepath.Join(iconsDir, "icons")
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		http.Error(w, "mkdir failed", http.StatusInternalServerError)
		return
	}
	dst := filepath.Join(dstDir, sanitize(packageName)+".png")

	if err := os.Rename(src, dst); err != nil {
		http.Error(w, "move failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`UPDATE apps SET icon_path = ?, updated_at = ? WHERE package_name = ?`,
		"icons/"+sanitize(packageName)+".png", now, packageName,
	); err != nil {
		http.Error(w, "db update failed", http.StatusInternalServerError)
		return
	}

	db.Exec("DELETE FROM pending_icons WHERE id = ?", id)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleReject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var userUUID, packageName string
	err := db.QueryRow("SELECT user_uuid, package_name FROM pending_icons WHERE id = ?", id).
		Scan(&userUUID, &packageName)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	os.Remove(filepath.Join(iconsDir, "pending-icons", userUUID+"-"+sanitize(packageName)+".png"))
	db.Exec("DELETE FROM pending_icons WHERE id = ?", id)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func sanitize(pkg string) string {
	var b strings.Builder
	for _, c := range pkg {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

var indexTmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Icon Review</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, sans-serif; background: #f5f5f5; padding: 2rem; }
  h1 { margin-bottom: 1.5rem; font-size: 1.25rem; color: #333; }
  .empty { color: #888; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 1rem; }
  .card {
    background: #fff; border-radius: 10px; padding: 1rem;
    box-shadow: 0 1px 4px rgba(0,0,0,.1); display: flex;
    flex-direction: column; align-items: center; gap: .75rem;
  }
  .card img { width: 80px; height: 80px; border-radius: 16px; object-fit: cover; }
  .card .name { font-weight: 600; font-size: .9rem; text-align: center; word-break: break-all; }
  .card .pkg { font-size: .72rem; color: #888; text-align: center; word-break: break-all; }
  .card .date { font-size: .72rem; color: #aaa; }
  .actions { display: flex; gap: .5rem; width: 100%; }
  .actions form { flex: 1; }
  .actions button {
    width: 100%; padding: .4rem; border: none; border-radius: 6px;
    cursor: pointer; font-size: .85rem; font-weight: 600;
  }
  .btn-approve { background: #22c55e; color: #fff; }
  .btn-approve:hover { background: #16a34a; }
  .btn-reject { background: #ef4444; color: #fff; }
  .btn-reject:hover { background: #dc2626; }
</style>
</head>
<body>
<h1>Pending Icons ({{len .}})</h1>
{{if .}}
<div class="grid">
{{range .}}
  <div class="card">
    <img src="/img/{{.FileName}}" alt="{{.PackageName}}">
    <div class="name">{{if .AppName}}{{.AppName}}{{else}}—{{end}}</div>
    <div class="pkg">{{.PackageName}}</div>
    <div class="date">{{.SubmittedAt}}</div>
    <div class="actions">
      <form method="POST" action="/approve/{{.ID}}">
        <button class="btn-approve" type="submit">Approve</button>
      </form>
      <form method="POST" action="/reject/{{.ID}}">
        <button class="btn-reject" type="submit">Reject</button>
      </form>
    </div>
  </div>
{{end}}
</div>
{{else}}
<p class="empty">No pending icons.</p>
{{end}}
</body>
</html>`))
