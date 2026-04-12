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

type ZeroApp struct {
	PackageName string
	AppName     string
	IconPath    string
	ReportCount int
}

type ZeroAppsPage struct {
	Apps        []ZeroApp
	NoIconCount int
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
	mux.HandleFunc("GET /zero-apps", handleZeroApps)
	mux.HandleFunc("POST /zero-apps/delete", handleDeleteZeroApp)
	mux.HandleFunc("POST /zero-apps/delete-all-no-icon", handleDeleteAllZeroNoIcon)
	mux.HandleFunc("GET /icon/{filename}", handleServeApprovedIcon)

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

func handleServeApprovedIcon(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, filepath.Join(iconsDir, "icons", filename))
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

func listZeroApps() ([]ZeroApp, error) {
	rows, err := db.Query(`
		SELECT au.package_name, COALESCE(a.app_name, ''), COALESCE(a.icon_path, ''), COUNT(*) AS report_count
		FROM app_usage au
		LEFT JOIN apps a ON a.package_name = au.package_name
		GROUP BY au.package_name
		HAVING SUM(au.total_used_minutes) = 0
		ORDER BY au.package_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []ZeroApp
	for rows.Next() {
		var z ZeroApp
		if err := rows.Scan(&z.PackageName, &z.AppName, &z.IconPath, &z.ReportCount); err != nil {
			return nil, err
		}
		apps = append(apps, z)
	}
	return apps, nil
}

func handleZeroApps(w http.ResponseWriter, r *http.Request) {
	apps, err := listZeroApps()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var noIconCount int
	for _, z := range apps {
		if z.IconPath == "" {
			noIconCount++
		}
	}
	if err := zeroAppsTmpl.Execute(w, ZeroAppsPage{Apps: apps, NoIconCount: noIconCount}); err != nil {
		log.Printf("template error: %v", err)
	}
}

func handleDeleteAllZeroNoIcon(w http.ResponseWriter, r *http.Request) {
	apps, err := listZeroApps()
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	for _, z := range apps {
		if z.IconPath != "" {
			continue
		}
		for _, q := range []string{
			`DELETE FROM app_usage WHERE package_name = ?`,
			`DELETE FROM app_limits WHERE package_name = ?`,
			`DELETE FROM pending_app_limits WHERE package_name = ?`,
			`DELETE FROM app_schedules WHERE package_name = ?`,
			`DELETE FROM pending_icons WHERE package_name = ?`,
			`DELETE FROM apps WHERE package_name = ?`,
		} {
			if _, err := tx.Exec(q, z.PackageName); err != nil {
				http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "commit error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/zero-apps", http.StatusSeeOther)
}

func handleDeleteZeroApp(w http.ResponseWriter, r *http.Request) {
	pkg := r.FormValue("package_name")
	if pkg == "" {
		http.Error(w, "missing package_name", http.StatusBadRequest)
		return
	}

	// Verify this app still qualifies (only zero-usage records) before deleting.
	var totalUsed int
	err := db.QueryRow(
		`SELECT COALESCE(SUM(total_used_minutes), 0) FROM app_usage WHERE package_name = ?`, pkg,
	).Scan(&totalUsed)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if totalUsed > 0 {
		http.Error(w, "app has non-zero usage, refusing to delete", http.StatusConflict)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM app_usage WHERE package_name = ?`,
		`DELETE FROM app_limits WHERE package_name = ?`,
		`DELETE FROM pending_app_limits WHERE package_name = ?`,
		`DELETE FROM app_schedules WHERE package_name = ?`,
		`DELETE FROM pending_icons WHERE package_name = ?`,
		`DELETE FROM apps WHERE package_name = ?`,
	} {
		if _, err := tx.Exec(q, pkg); err != nil {
			http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "commit error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/zero-apps", http.StatusSeeOther)
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

const sharedCSS = `
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, sans-serif; background: #f5f5f5; padding: 2rem; }
  nav { display: flex; gap: 1rem; margin-bottom: 1.5rem; }
  nav a {
    padding: .4rem .9rem; border-radius: 6px; text-decoration: none;
    font-size: .9rem; font-weight: 600; background: #e5e7eb; color: #374151;
  }
  nav a.active { background: #374151; color: #fff; }
  nav a:hover:not(.active) { background: #d1d5db; }
  h1 { margin-bottom: 1.5rem; font-size: 1.25rem; color: #333; }
  .empty { color: #888; }
`

var indexTmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Icon Review</title>
<style>` + sharedCSS + `
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
<nav>
  <a href="/" class="active">Icon Review</a>
  <a href="/zero-apps">Zero-Time Apps</a>
</nav>
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

var zeroAppsTmpl = template.Must(template.New("zero-apps").Funcs(template.FuncMap{
	"iconFile": func(iconPath string) string {
		return filepath.Base(iconPath)
	},
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Zero-Time Apps</title>
<style>` + sharedCSS + `
  table { width: 100%; border-collapse: collapse; background: #fff; border-radius: 10px; overflow: hidden; box-shadow: 0 1px 4px rgba(0,0,0,.1); }
  th { background: #f9fafb; text-align: left; padding: .6rem 1rem; font-size: .8rem; color: #6b7280; text-transform: uppercase; letter-spacing: .04em; border-bottom: 1px solid #e5e7eb; }
  td { padding: .65rem 1rem; font-size: .88rem; border-bottom: 1px solid #f3f4f6; vertical-align: middle; }
  tr:last-child td { border-bottom: none; }
  .pkg { color: #888; font-size: .78rem; word-break: break-all; }
  .count { color: #6b7280; }
  .btn-delete {
    padding: .3rem .8rem; border: none; border-radius: 6px;
    background: #ef4444; color: #fff; cursor: pointer;
    font-size: .82rem; font-weight: 600;
  }
  .btn-delete:hover { background: #dc2626; }
  .overlay {
    display: none; position: fixed; inset: 0;
    background: rgba(0,0,0,.4); align-items: center; justify-content: center; z-index: 10;
  }
  .overlay.open { display: flex; }
  .modal {
    background: #fff; border-radius: 12px; padding: 1.5rem;
    box-shadow: 0 8px 32px rgba(0,0,0,.2); max-width: 360px; width: 100%; margin: 1rem;
  }
  .modal h2 { font-size: 1rem; margin-bottom: .5rem; color: #111; }
  .modal p { font-size: .875rem; color: #6b7280; margin-bottom: 1.25rem; word-break: break-all; }
  .modal-actions { display: flex; gap: .5rem; justify-content: flex-end; }
  .btn-cancel {
    padding: .4rem .9rem; border: 1px solid #d1d5db; border-radius: 6px;
    background: #fff; color: #374151; cursor: pointer; font-size: .875rem; font-weight: 600;
  }
  .btn-cancel:hover { background: #f3f4f6; }
  .btn-confirm-delete {
    padding: .4rem .9rem; border: none; border-radius: 6px;
    background: #ef4444; color: #fff; cursor: pointer; font-size: .875rem; font-weight: 600;
  }
  .btn-confirm-delete:hover { background: #dc2626; }
  .btn-delete-all {
    padding: .4rem .9rem; border: none; border-radius: 6px;
    background: #ef4444; color: #fff; cursor: pointer; font-size: .875rem; font-weight: 600;
  }
  .btn-delete-all:hover { background: #dc2626; }
</style>
</head>
<body>
<nav>
  <a href="/">Icon Review</a>
  <a href="/zero-apps" class="active">Zero-Time Apps</a>
</nav>
<div style="display:flex;align-items:center;gap:1rem;margin-bottom:1.5rem;">
  <h1 style="margin:0;">Zero-Time Apps ({{len .Apps}})</h1>
  {{if .NoIconCount}}
  <button class="btn-delete-all" onclick="openDeleteAllModal()">Delete all without icon ({{.NoIconCount}})</button>
  {{end}}
</div>
{{if .Apps}}
<table>
  <thead>
    <tr>
      <th>App</th>
      <th>Reports</th>
      <th></th>
    </tr>
  </thead>
  <tbody>
  {{range .Apps}}
    <tr>
      <td style="display:flex;align-items:center;gap:.75rem;">
        {{if .IconPath}}<img src="/icon/{{iconFile .IconPath}}" width="40" height="40" style="border-radius:8px;flex-shrink:0;">{{else}}<div style="width:40px;height:40px;border-radius:8px;background:#e5e7eb;flex-shrink:0;"></div>{{end}}
        <div>
          {{if .AppName}}<strong>{{.AppName}}</strong><br>{{end}}
          <span class="pkg">{{.PackageName}}</span>
        </div>
      </td>
      <td class="count">{{.ReportCount}}</td>
      <td>
        <button class="btn-delete" data-pkg="{{.PackageName}}" data-name="{{.AppName}}" onclick="openModal(this)">Delete</button>
      </td>
    </tr>
  {{end}}
  </tbody>
</table>
{{else}}
<p class="empty">No apps with zero-only usage records.</p>
{{end}}

<div class="overlay" id="overlay" onclick="closeModal(event)">
  <div class="modal">
    <h2>Delete app?</h2>
    <p id="modal-body"></p>
    <div class="modal-actions">
      <button class="btn-cancel" onclick="closeModal()">Cancel</button>
      <form method="POST" action="/zero-apps/delete" style="margin:0">
        <input type="hidden" name="package_name" id="modal-pkg">
        <button class="btn-confirm-delete" type="submit">Delete</button>
      </form>
    </div>
  </div>
</div>

<div class="overlay" id="overlay-all" onclick="closeDeleteAllModal(event)">
  <div class="modal">
    <h2>Delete all apps without icon?</h2>
    <p>All <strong>{{.NoIconCount}}</strong> apps with zero usage and no icon will be removed from all records. This cannot be undone.</p>
    <div class="modal-actions">
      <button class="btn-cancel" onclick="closeDeleteAllModal()">Cancel</button>
      <form method="POST" action="/zero-apps/delete-all-no-icon" style="margin:0">
        <button class="btn-confirm-delete" type="submit">Delete all</button>
      </form>
    </div>
  </div>
</div>

<script>
function openModal(btn) {
  var pkg = btn.dataset.pkg;
  var name = btn.dataset.name;
  document.getElementById('modal-pkg').value = pkg;
  document.getElementById('modal-body').textContent =
    (name ? name + ' (' + pkg + ')' : pkg) + ' will be removed from all records.';
  document.getElementById('overlay').classList.add('open');
}
function closeModal(e) {
  if (e && e.target !== document.getElementById('overlay')) return;
  document.getElementById('overlay').classList.remove('open');
}
function openDeleteAllModal() {
  document.getElementById('overlay-all').classList.add('open');
}
function closeDeleteAllModal(e) {
  if (e && e.target !== document.getElementById('overlay-all')) return;
  document.getElementById('overlay-all').classList.remove('open');
}
</script>
</body>
</html>`))
