package main

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// --- Dashboard ---

func (wh *WebHandler) showDashboard(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	children := wh.listChildren(parentUUID)
	renderPage(w, "dashboard-page", map[string]interface{}{
		"Children": children,
	})
}

func (wh *WebHandler) showChild(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Redirect(w, r, "/web/dashboard", http.StatusSeeOther)
		return
	}

	var child User
	var name sql.NullString
	err := wh.DB.QueryRow("SELECT uuid, name, role FROM users WHERE uuid = ?", childUUID).Scan(&child.UUID, &name, &child.Role)
	if err != nil {
		http.Redirect(w, r, "/web/dashboard", http.StatusSeeOther)
		return
	}
	child.Name = name.String

	// Check if reports exist but none in the last 24h
	var hasAnyReport bool
	wh.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM app_usage WHERE user_uuid = ?)", childUUID).Scan(&hasAnyReport)

	var staleReport bool
	if hasAnyReport {
		yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
		var hasRecentReport bool
		wh.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM app_usage WHERE user_uuid = ? AND usage_date >= ?)", childUUID, yesterday).Scan(&hasRecentReport)
		staleReport = !hasRecentReport
	}

	renderPage(w, "child-page", map[string]interface{}{
		"Child":       child,
		"StaleReport": staleReport,
	})
}

func (wh *WebHandler) showChildSettings(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Redirect(w, r, "/web/dashboard", http.StatusSeeOther)
		return
	}

	var child User
	var name sql.NullString
	err := wh.DB.QueryRow("SELECT uuid, name, role FROM users WHERE uuid = ?", childUUID).Scan(&child.UUID, &name, &child.Role)
	if err != nil {
		http.Redirect(w, r, "/web/dashboard", http.StatusSeeOther)
		return
	}
	child.Name = name.String

	delayed := isDelayedChangesEnabled(wh.DB, childUUID)

	renderPage(w, "child-settings-page", map[string]interface{}{
		"Child":          child,
		"DelayedChanges": delayed,
	})
}

func (wh *WebHandler) toggleDelayedChanges(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	enabled := r.FormValue("enabled") == "on"
	wh.DB.Exec("UPDATE users SET delayed_changes = ? WHERE uuid = ?", enabled, childUUID)

	// If disabling, apply all pending limits immediately
	if !enabled {
		applyPendingLimits(wh.DB, childUUID)
		// Delete any remaining pending limits (future ones)
		wh.DB.Exec("DELETE FROM pending_app_limits WHERE user_uuid = ?", childUUID)
	}

	renderPartial(w, "delayed-changes-toggle", map[string]interface{}{
		"Child":          User{UUID: childUUID},
		"DelayedChanges": enabled,
	})
}

func (wh *WebHandler) showChildWelcome(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Redirect(w, r, "/web/dashboard", http.StatusSeeOther)
		return
	}

	var child User
	var name sql.NullString
	err := wh.DB.QueryRow("SELECT uuid, name, role FROM users WHERE uuid = ?", childUUID).Scan(&child.UUID, &name, &child.Role)
	if err != nil {
		http.Redirect(w, r, "/web/dashboard", http.StatusSeeOther)
		return
	}
	child.Name = name.String

	token, expiresAt, err := wh.latestToken(childUUID)
	if err != nil || time.Now().After(expiresAt) {
		http.Redirect(w, r, "/web/children/"+childUUID, http.StatusSeeOther)
		return
	}

	renderPage(w, "child-welcome-page", map[string]interface{}{
		"Child":     child,
		"Token":     token,
		"ExpiresIn": formatDuration(time.Until(expiresAt)),
	})
}

func (wh *WebHandler) checkWelcomeStatus(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var child User
	var name sql.NullString
	wh.DB.QueryRow("SELECT uuid, name, role FROM users WHERE uuid = ?", childUUID).Scan(&child.UUID, &name, &child.Role)
	child.Name = name.String

	token, expiresAt, err := wh.latestToken(childUUID)

	if err != nil {
		// Token gone (used) — redirect to child page
		w.Header().Set("HX-Redirect", "/web/children/"+childUUID)
		return
	}

	if time.Now().After(expiresAt) {
		// Token expired — delete it and show expired state
		wh.DB.Exec("DELETE FROM auth_tokens WHERE token = ?", token)
		renderPartial(w, "welcome-token-expired", map[string]interface{}{
			"Child": child,
		})
		return
	}

	// Token still active — keep showing it
	renderPartial(w, "welcome-token-active", map[string]interface{}{
		"Child":     child,
		"Token":     token,
		"ExpiresIn": formatDuration(time.Until(expiresAt)),
	})
}

// --- Children ---

func (wh *WebHandler) createChild(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	childUUID := uuid.New().String()
	wh.DB.Exec("INSERT INTO users (uuid, name, role) VALUES (?, ?, 'child')", childUUID, name)
	wh.DB.Exec("INSERT INTO parent_children (parent_uuid, child_uuid) VALUES (?, ?)", parentUUID, childUUID)

	// Generate a login token for the child
	token, _ := generateToken()
	expiresAt := time.Now().Add(10 * time.Minute)
	wh.DB.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) VALUES (?, ?, ?)", token, childUUID, expiresAt)

	w.Header().Set("HX-Redirect", "/web/children/"+childUUID+"/welcome")
}

func (wh *WebHandler) deleteChild(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	wh.DB.Exec(
		"DELETE FROM users WHERE uuid = ? AND uuid IN (SELECT child_uuid FROM parent_children WHERE parent_uuid = ?)",
		childUUID, parentUUID,
	)

	children := wh.listChildren(parentUUID)
	renderPartial(w, "children-list", map[string]interface{}{"Children": children})
}

func (wh *WebHandler) generateChildToken(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	token, _ := generateToken()
	expiresAt := time.Now().Add(10 * time.Minute)
	wh.DB.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) VALUES (?, ?, ?)", token, childUUID, expiresAt)

	renderPartial(w, "child-token", map[string]string{
		"ChildUUID": childUUID,
		"Token":     token,
		"ExpiresAt": expiresAt.Format("15:04:05"),
	})
}

func (wh *WebHandler) checkChildTokenStatus(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var token string
	var expiresAt time.Time
	err := wh.DB.QueryRow(
		"SELECT token, expires_at FROM auth_tokens WHERE user_uuid = ? ORDER BY expires_at DESC LIMIT 1",
		childUUID,
	).Scan(&token, &expiresAt)

	if err != nil || time.Now().After(expiresAt) {
		// Token expired or used (deleted) — show the button again
		renderPartial(w, "child-token-button", map[string]string{"ChildUUID": childUUID})
		return
	}

	// Token still valid — keep showing it
	renderPartial(w, "child-token", map[string]string{
		"ChildUUID": childUUID,
		"Token":     token,
		"ExpiresAt": expiresAt.Format("15:04:05"),
	})
}

func (wh *WebHandler) latestToken(childUUID string) (string, time.Time, error) {
	var token string
	var expiresAt time.Time
	err := wh.DB.QueryRow(
		"SELECT token, expires_at FROM auth_tokens WHERE user_uuid = ? ORDER BY expires_at DESC LIMIT 1",
		childUUID,
	).Scan(&token, &expiresAt)
	return token, expiresAt, err
}

func (wh *WebHandler) ownsChild(parentUUID, childUUID string) bool {
	var exists bool
	wh.DB.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM parent_children WHERE parent_uuid = ? AND child_uuid = ?)",
		parentUUID, childUUID,
	).Scan(&exists)
	return exists
}

func (wh *WebHandler) listChildren(parentUUID string) []User {
	rows, err := wh.DB.Query(
		`SELECT u.uuid, u.name, u.role FROM users u
		 INNER JOIN parent_children pc ON pc.child_uuid = u.uuid
		 WHERE pc.parent_uuid = ?`, parentUUID,
	)
	if err != nil {
		return []User{}
	}
	defer rows.Close()

	var list []User
	for rows.Next() {
		var u User
		var name sql.NullString
		rows.Scan(&u.UUID, &name, &u.Role)
		u.Name = name.String
		list = append(list, u)
	}
	if list == nil {
		list = []User{}
	}
	return list
}
