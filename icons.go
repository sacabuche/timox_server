package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const maxIconUploadBytes = 512 * 1024 // 512 KB

// POST /icons/check
// Body: ["com.example.app", ...]
// Returns: ["com.example.app", ...] — packages that:
//   - the user has previously reported via /report
//   - do not yet have an approved icon in the apps table
//   - this user has not already submitted a pending icon for
func handleIconsCheck(w http.ResponseWriter, r *http.Request) {
	userUUID := r.Context().Value(CtxUserUUID).(string)

	var packages []string
	if err := json.NewDecoder(r.Body).Decode(&packages); err != nil || len(packages) == 0 {
		http.Error(w, "expected non-empty JSON array of package names", http.StatusBadRequest)
		return
	}

	// Build placeholders for the IN clause
	placeholders := make([]string, len(packages))
	args := make([]interface{}, 0, len(packages)+2)
	args = append(args, userUUID)
	for i, pkg := range packages {
		placeholders[i] = "?"
		args = append(args, pkg)
	}
	args = append(args, userUUID)

	query := `
		SELECT DISTINCT au.package_name
		FROM app_usage au
		WHERE au.user_uuid = ?
		  AND au.package_name IN (` + strings.Join(placeholders, ",") + `)
		  AND au.package_name NOT IN (SELECT package_name FROM apps WHERE icon_path IS NOT NULL)
		  AND NOT EXISTS (
		      SELECT 1 FROM pending_icons pi
		      WHERE pi.user_uuid = ? AND pi.package_name = au.package_name
		  )`

	rows, err := db.Query(query, args...)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	needed := make([]string, 0)
	for rows.Next() {
		var pkg string
		if err := rows.Scan(&pkg); err == nil {
			needed = append(needed, pkg)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(needed)
}

// POST /icons
// Body: {"packageName": "com.example.app", "appName": "Example", "iconBase64": "<png bytes in base64>"}
// Saves file to $ICONS_DIR/pending-icons/{userUUID}-{packageName}.png and records in pending_icons.
func handleIconUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxIconUploadBytes)

	userUUID := r.Context().Value(CtxUserUUID).(string)

	var body struct {
		PackageName string `json:"packageName"`
		AppName     string `json:"appName"`
		IconBase64  string `json:"iconBase64"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.PackageName == "" || body.IconBase64 == "" {
		http.Error(w, "packageName and iconBase64 are required", http.StatusBadRequest)
		return
	}

	iconBytes, err := base64.StdEncoding.DecodeString(body.IconBase64)
	if err != nil {
		// Try URL-safe base64 as fallback
		iconBytes, err = base64.RawStdEncoding.DecodeString(body.IconBase64)
		if err != nil {
			http.Error(w, "iconBase64 is not valid base64", http.StatusBadRequest)
			return
		}
	}

	pendingDir := filepath.Join(cfg.IconsDir, "pending-icons")
	if err := os.MkdirAll(pendingDir, 0755); err != nil {
		http.Error(w, "failed to create pending-icons directory", http.StatusInternalServerError)
		return
	}

	fileName := userUUID + "-" + sanitizePackageName(body.PackageName) + ".png"
	filePath := filepath.Join(pendingDir, fileName)
	if err := os.WriteFile(filePath, iconBytes, 0644); err != nil {
		http.Error(w, "failed to save icon", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := uuid.New().String()
	_, err = db.Exec(
		`INSERT INTO pending_icons (id, user_uuid, package_name, submitted_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_uuid, package_name) DO UPDATE SET
		   id = excluded.id,
		   submitted_at = excluded.submitted_at`,
		id, userUUID, body.PackageName, now,
	)
	if err != nil {
		os.Remove(filePath)
		http.Error(w, "failed to record pending icon", http.StatusInternalServerError)
		return
	}

	// Upsert app_name into apps table (create row without icon_path if not already there)
	if body.AppName != "" {
		_, _ = db.Exec(
			`INSERT INTO apps (package_name, app_name, created_at, updated_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(package_name) DO UPDATE SET
			   app_name = COALESCE(excluded.app_name, apps.app_name),
			   updated_at = excluded.updated_at`,
			body.PackageName, body.AppName, now, now,
		)
	}

	logFromCtx(r.Context()).Info("icon uploaded", "package", body.PackageName, "app", body.AppName, "bytes", len(iconBytes))

	w.WriteHeader(http.StatusNoContent)
}

// GET /apps/{packages}
// {packages} is a comma-separated list of package names.
// Returns JSON array of app info for packages found in the apps table.
func handleGetApps(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "packages")
	if raw == "" {
		http.Error(w, "packages param is required", http.StatusBadRequest)
		return
	}

	pkgs := strings.Split(raw, ",")
	if len(pkgs) == 0 {
		http.Error(w, "packages param is empty", http.StatusBadRequest)
		return
	}

	type AppInfo struct {
		PackageName string  `json:"packageName"`
		AppName     *string `json:"appName"`
		IconPath    *string `json:"iconPath"`
	}

	result := make([]AppInfo, 0, len(pkgs))
	for _, pkg := range pkgs {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		var info AppInfo
		info.PackageName = pkg
		err := db.QueryRow(
			`SELECT app_name, icon_path FROM apps WHERE package_name = ?`, pkg,
		).Scan(&info.AppName, &info.IconPath)
		if err != nil {
			// Not found — skip
			continue
		}
		result = append(result, info)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// sanitizePackageName strips characters that are not safe for filenames.
// Android package names are dot-separated identifiers, so only dots, letters, and digits are kept.
func sanitizePackageName(pkg string) string {
	var b strings.Builder
	for _, c := range pkg {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' {
			b.WriteRune(c)
		}
	}
	return b.String()
}
