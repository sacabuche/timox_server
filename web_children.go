package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// --- Dashboard ---

func (wh *WebHandler) showDashboard(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	children := wh.listChildren(parentUUID)
	renderPage(w, r, "dashboard-page", map[string]interface{}{
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

	renderPage(w, r, "child-page", map[string]interface{}{
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
	disableAt := delayedChangesDisableAt(wh.DB, childUUID)

	var scheduleStart, scheduleEnd sql.NullString
	wh.DB.QueryRow("SELECT blocking_start_time, blocking_end_time FROM users WHERE uuid = ?", childUUID).Scan(&scheduleStart, &scheduleEnd)

	var totalLimitMinutes int
	var hasTotalLimit bool
	var rawTotal sql.NullInt64
	if err := wh.DB.QueryRow("SELECT daily_limit_minutes FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, GlobalTimePkg).Scan(&rawTotal); err == nil {
		totalLimitMinutes = int(rawTotal.Int64)
		hasTotalLimit = true
	}

	hasSchedule := scheduleStart.Valid && scheduleEnd.Valid
	renderPage(w, r, "child-settings-page", map[string]interface{}{
		"Child":             child,
		"DelayedChanges":    delayed,
		"DisableAt":         disableAt,
		"Checked":           delayed && disableAt == nil,
		"ScheduleStart":     scheduleStart.String,
		"ScheduleEnd":       scheduleEnd.String,
		"HasSchedule":       hasSchedule,
		"BlockedHours":      scheduleBlockedDuration(scheduleStart.String, scheduleEnd.String),
		"TotalLimitMinutes": totalLimitMinutes,
		"HasTotalLimit":     hasTotalLimit,
		"BuildDate":         buildDate,
		"CommitSHA":         commitSHA,
	})
}

func (wh *WebHandler) saveTotalDailyLimit(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	remove := r.FormValue("remove") == "1"
	if remove {
		wh.DB.Exec("DELETE FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, GlobalTimePkg)
	} else {
		limit, err := strconv.Atoi(r.FormValue("total_limit_minutes"))
		if err != nil || limit <= 0 {
			http.Error(w, "invalid total_limit_minutes", http.StatusBadRequest)
			return
		}
		wh.DB.Exec(
			`INSERT INTO app_limits (user_uuid, package_name, daily_limit_minutes, block_type)
			 VALUES (?, ?, ?, 0)
			 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes`,
			childUUID, GlobalTimePkg, limit,
		)
	}

	var totalLimitMinutes int
	var hasTotalLimit bool
	var rawTotal sql.NullInt64
	if err := wh.DB.QueryRow("SELECT daily_limit_minutes FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, GlobalTimePkg).Scan(&rawTotal); err == nil {
		totalLimitMinutes = int(rawTotal.Int64)
		hasTotalLimit = true
	}

	renderPartial(w, r, "total-daily-limit-form", map[string]interface{}{
		"Child":             User{UUID: childUUID},
		"TotalLimitMinutes": totalLimitMinutes,
		"HasTotalLimit":     hasTotalLimit,
	})
}

func (wh *WebHandler) saveChildGlobalSchedule(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	start := strings.TrimSpace(r.FormValue("schedule_start"))
	end := strings.TrimSpace(r.FormValue("schedule_end"))

	if start != "" || end != "" {
		if !isValidTime(start) || !isValidTime(end) {
			http.Error(w, "invalid time format, use HH:MM", http.StatusBadRequest)
			return
		}
		wh.DB.Exec("UPDATE users SET blocking_start_time = ?, blocking_end_time = ? WHERE uuid = ?", start, end, childUUID)
	} else {
		wh.DB.Exec("UPDATE users SET blocking_start_time = NULL, blocking_end_time = NULL WHERE uuid = ?", childUUID)
	}

	var scheduleStart, scheduleEnd sql.NullString
	wh.DB.QueryRow("SELECT blocking_start_time, blocking_end_time FROM users WHERE uuid = ?", childUUID).Scan(&scheduleStart, &scheduleEnd)

	hasSchedule := scheduleStart.Valid && scheduleEnd.Valid
	renderPartial(w, r, "global-schedule-form", map[string]interface{}{
		"Child":         User{UUID: childUUID},
		"ScheduleStart": scheduleStart.String,
		"ScheduleEnd":   scheduleEnd.String,
		"HasSchedule":   hasSchedule,
		"BlockedHours":  scheduleBlockedDuration(scheduleStart.String, scheduleEnd.String),
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

	if !enabled {
		// Schedule disable for 24h from now instead of applying immediately
		disableAt := time.Now().Add(24 * time.Hour)
		wh.DB.Exec("UPDATE users SET delayed_changes_disable_at = ? WHERE uuid = ?", disableAt, childUUID)
	} else {
		// Re-enabling: clear any pending disable
		wh.DB.Exec("UPDATE users SET delayed_changes = true, delayed_changes_disable_at = NULL WHERE uuid = ?", childUUID)
	}

	delayed := isDelayedChangesEnabled(wh.DB, childUUID)
	disableAt := delayedChangesDisableAt(wh.DB, childUUID)
	renderPartial(w, r, "delayed-changes-toggle", map[string]interface{}{
		"Child":          User{UUID: childUUID},
		"DelayedChanges": delayed,
		"DisableAt":      disableAt,
		"Checked":        delayed && disableAt == nil,
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

	renderPage(w, r, "child-welcome-page", map[string]interface{}{
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
		renderPartial(w, r, "welcome-token-expired", map[string]interface{}{
			"Child": child,
		})
		return
	}

	// Token still active — keep showing it
	renderPartial(w, r, "welcome-token-active", map[string]interface{}{
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
	renderPartial(w, r, "children-list", map[string]interface{}{"Children": children})
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

	renderPartial(w, r, "child-token", map[string]interface{}{
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
		renderPartial(w, r, "child-token-button", map[string]interface{}{"ChildUUID": childUUID})
		return
	}

	// Token still valid — keep showing it
	renderPartial(w, r, "child-token", map[string]interface{}{
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

type ChildSummary struct {
	UUID       string
	Name       string
	UnlockTime string // non-empty = currently in a global blocking window
}

func (wh *WebHandler) listChildren(parentUUID string) []ChildSummary {
	rows, err := wh.DB.Query(
		`SELECT u.uuid, u.name, u.blocking_start_time, u.blocking_end_time
		 FROM users u
		 INNER JOIN parent_children pc ON pc.child_uuid = u.uuid
		 WHERE pc.parent_uuid = ?`, parentUUID,
	)
	if err != nil {
		return []ChildSummary{}
	}
	defer rows.Close()

	var list []ChildSummary
	for rows.Next() {
		var cs ChildSummary
		var name, start, end sql.NullString
		rows.Scan(&cs.UUID, &name, &start, &end)
		cs.Name = name.String
		if start.Valid && end.Valid {
			cs.UnlockTime = scheduleUnlockTime(start.String, end.String)
		}
		list = append(list, cs)
	}
	if list == nil {
		list = []ChildSummary{}
	}
	return list
}
