package main

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// --- Pending limits helpers ---

// applyPendingLimits promotes any pending_app_limits whose applies_at has passed
// into active app_limits, then deletes them from the pending table.
func applyPendingLimits(db *sql.DB, childUUID string) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	rows, err := db.Query(
		"SELECT package_name, daily_limit_minutes, blocked FROM pending_app_limits WHERE user_uuid = ? AND applies_at <= ?",
		childUUID, now,
	)
	if err != nil || rows == nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var pkg string
		var limit int
		var blocked bool
		rows.Scan(&pkg, &limit, &blocked)

		db.Exec(
			`INSERT INTO app_limits (user_uuid, package_name, daily_limit_minutes, blocked)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes, blocked = excluded.blocked`,
			childUUID, pkg, limit, blocked,
		)
	}

	db.Exec("DELETE FROM pending_app_limits WHERE user_uuid = ? AND applies_at <= ?", childUUID, now)
}

func isDelayedChangesEnabled(db *sql.DB, childUUID string) bool {
	var enabled bool
	var disableAt *time.Time
	db.QueryRow("SELECT delayed_changes, delayed_changes_disable_at FROM users WHERE uuid = ?", childUUID).Scan(&enabled, &disableAt)
	if enabled && disableAt != nil && time.Now().After(*disableAt) {
		db.Exec("UPDATE users SET delayed_changes = false, delayed_changes_disable_at = NULL WHERE uuid = ?", childUUID)
		applyPendingLimits(db, childUUID)
		db.Exec("DELETE FROM pending_app_limits WHERE user_uuid = ?", childUUID)
		return false
	}
	return enabled
}

// delayedChangesDisableAt returns the scheduled disable time, or nil if not pending.
func delayedChangesDisableAt(db *sql.DB, childUUID string) *time.Time {
	var disableAt *time.Time
	db.QueryRow("SELECT delayed_changes_disable_at FROM users WHERE uuid = ?", childUUID).Scan(&disableAt)
	return disableAt
}

// --- Apps (merged usage + limits) ---

func (wh *WebHandler) getChildApps(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	applyPendingLimits(wh.DB, childUUID)
	wh.renderApps(w, r, childUUID)
}

func (wh *WebHandler) editChildLimit(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")
	packageName := chi.URLParam(r, "packageName")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var dailyLimit int
	var blocked bool
	var hasLimit bool
	err := wh.DB.QueryRow("SELECT daily_limit_minutes, blocked FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName).Scan(&dailyLimit, &blocked)
	if err == nil {
		hasLimit = true
	}

	// Check for pending limit
	var pendingLimit int
	var pendingAppliesAt time.Time
	var hasPending bool
	err = wh.DB.QueryRow("SELECT daily_limit_minutes, applies_at FROM pending_app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName).Scan(&pendingLimit, &pendingAppliesAt)
	if err == nil {
		hasPending = true
	}

	// Check for existing schedule
	var scheduleStart, scheduleEnd string
	var hasSchedule bool
	err = wh.DB.QueryRow("SELECT blocking_start_time, blocking_end_time FROM app_schedules WHERE user_uuid = ? AND package_name = ?", childUUID, packageName).Scan(&scheduleStart, &scheduleEnd)
	if err == nil {
		hasSchedule = true
	}

	var appName string
	wh.DB.QueryRow("SELECT COALESCE(app_name, '') FROM app_usage WHERE user_uuid = ? AND package_name = ? ORDER BY usage_date DESC LIMIT 1", childUUID, packageName).Scan(&appName)

	delayed := isDelayedChangesEnabled(wh.DB, childUUID)

	displayName := appName
	if displayName == "" {
		displayName = packageName
	}

	data := map[string]interface{}{
		"ChildUUID":         childUUID,
		"PackageName":       packageName,
		"DisplayName":       displayName,
		"DailyLimitMinutes": dailyLimit,
		"HasLimit":          hasLimit,
		"Blocked":           blocked,
		"DelayedChanges":    delayed,
		"HasPending":        hasPending,
		"ScheduleStart":        scheduleStart,
		"ScheduleEnd":          scheduleEnd,
		"HasSchedule":          hasSchedule,
		"ScheduleBlockedHours": scheduleBlockedDuration(scheduleStart, scheduleEnd),
	}
	if hasPending {
		data["PendingLimit"] = pendingLimit
		data["PendingAppliesAt"] = pendingAppliesAt.Format("Jan 2, 15:04")
	}

	renderPartial(w, r, "limit-edit-modal", data)
}

func (wh *WebHandler) confirmDeleteChildLimit(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")
	packageName := chi.URLParam(r, "packageName")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	renderPartial(w, r, "limit-confirm-delete", map[string]interface{}{
		"ChildUUID":      childUUID,
		"PackageName":    packageName,
		"DelayedChanges": isDelayedChangesEnabled(wh.DB, childUUID),
	})
}

func (wh *WebHandler) addChildLimit(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	packageName := strings.TrimSpace(r.FormValue("package_name"))
	dailyLimit := r.FormValue("daily_limit_minutes")
	blocked := r.FormValue("blocked") == "on"

	if packageName == "" || (!blocked && dailyLimit == "") {
		http.Error(w, "package_name and daily_limit_minutes required", http.StatusBadRequest)
		return
	}

	if blocked {
		// Blocking is always immediate — also clear any pending increase
		if dailyLimit == "" {
			dailyLimit = "0"
		}
		wh.DB.Exec(
			`INSERT INTO app_limits (user_uuid, package_name, daily_limit_minutes, blocked)
			 VALUES (?, ?, ?, true)
			 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes, blocked = true`,
			childUUID, packageName, dailyLimit,
		)
		wh.DB.Exec("DELETE FROM pending_app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName)
	} else {
		newLimit, _ := strconv.Atoi(dailyLimit)

		// Check if delayed changes is enabled and this is an increase
		delayed := isDelayedChangesEnabled(wh.DB, childUUID)
		var currentLimit int
		var currentBlocked bool
		var hasExisting bool
		err := wh.DB.QueryRow("SELECT daily_limit_minutes, blocked FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName).Scan(&currentLimit, &currentBlocked)
		if err == nil {
			hasExisting = true
		}

		isIncrease := hasExisting && !currentBlocked && newLimit > currentLimit

		if delayed && isIncrease {
			// Store as pending with 24h delay
			appliesAt := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02 15:04:05")
			wh.DB.Exec(
				`INSERT INTO pending_app_limits (user_uuid, package_name, daily_limit_minutes, blocked, applies_at)
				 VALUES (?, ?, ?, false, ?)
				 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes, blocked = excluded.blocked, applies_at = excluded.applies_at`,
				childUUID, packageName, newLimit, appliesAt,
			)
		} else {
			// Apply immediately (decrease, new limit, or delayed changes off)
			wh.DB.Exec(
				`INSERT INTO app_limits (user_uuid, package_name, daily_limit_minutes, blocked)
				 VALUES (?, ?, ?, false)
				 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes, blocked = false`,
				childUUID, packageName, dailyLimit,
			)
			// Clear any pending limit for this app
			wh.DB.Exec("DELETE FROM pending_app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName)
		}
	}

	// Handle schedule
	scheduleStart := strings.TrimSpace(r.FormValue("schedule_start"))
	scheduleEnd := strings.TrimSpace(r.FormValue("schedule_end"))
	removeSchedule := r.FormValue("remove_schedule") == "1"
	if removeSchedule {
		wh.DB.Exec("DELETE FROM app_schedules WHERE user_uuid = ? AND package_name = ?", childUUID, packageName)
	} else if scheduleStart != "" || scheduleEnd != "" {
		if !isValidTime(scheduleStart) || !isValidTime(scheduleEnd) {
			http.Error(w, "invalid schedule time format, use HH:MM", http.StatusBadRequest)
			return
		}
		wh.DB.Exec(
			`INSERT INTO app_schedules (user_uuid, package_name, blocking_start_time, blocking_end_time)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(user_uuid, package_name) DO UPDATE SET blocking_start_time = excluded.blocking_start_time, blocking_end_time = excluded.blocking_end_time`,
			childUUID, packageName, scheduleStart, scheduleEnd,
		)
	}

	applyPendingLimits(wh.DB, childUUID)
	wh.renderApps(w, r, childUUID)
}

func (wh *WebHandler) deleteChildLimit(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")
	packageName := chi.URLParam(r, "packageName")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Removing a limit is immediate — also clear any pending
	wh.DB.Exec("DELETE FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName)
	wh.DB.Exec("DELETE FROM pending_app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName)
	wh.renderApps(w, r, childUUID)
}

func (wh *WebHandler) deleteChildSchedule(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")
	packageName := chi.URLParam(r, "packageName")

	if !wh.ownsChild(parentUUID, childUUID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	wh.DB.Exec("DELETE FROM app_schedules WHERE user_uuid = ? AND package_name = ?", childUUID, packageName)
	wh.renderApps(w, r, childUUID)
}

func (wh *WebHandler) renderApps(w http.ResponseWriter, r *http.Request, childUUID string) {
	type AppRow struct {
		PackageName       string
		AppName           string
		DisplayName       string
		TotalUsedMinutes  int
		UsedToday         bool
		DailyLimitMinutes int
		HasLimit          bool
		Blocked           bool
		Remaining         int
		OverLimit         bool
		PendingLimit      int
		HasPending        bool
		PendingIn         string
		ScheduleStart     string
		ScheduleEnd       string
		HasSchedule       bool
		UnlockTime        string // non-empty = currently blocked by schedule
	}

	today := time.Now().Format("2006-01-02")
	apps := map[string]*AppRow{}

	// Global schedule
	var globalStart, globalEnd sql.NullString
	wh.DB.QueryRow("SELECT blocking_start_time, blocking_end_time FROM users WHERE uuid = ?", childUUID).Scan(&globalStart, &globalEnd)
	globalScheduleStr := ""
	globalUnlockTime := ""
	if globalStart.Valid && globalEnd.Valid {
		globalScheduleStr = globalStart.String + " – " + globalEnd.String
		globalUnlockTime = scheduleUnlockTime(globalStart.String, globalEnd.String)
	}

	// Load limits
	limRows, _ := wh.DB.Query("SELECT package_name, daily_limit_minutes, blocked FROM app_limits WHERE user_uuid = ?", childUUID)
	if limRows != nil {
		defer limRows.Close()
		for limRows.Next() {
			a := &AppRow{}
			limRows.Scan(&a.PackageName, &a.DailyLimitMinutes, &a.Blocked)
			a.HasLimit = true
			apps[a.PackageName] = a
		}
	}

	// Load pending limits
	pendingRows, _ := wh.DB.Query("SELECT package_name, daily_limit_minutes, applies_at FROM pending_app_limits WHERE user_uuid = ?", childUUID)
	if pendingRows != nil {
		defer pendingRows.Close()
		for pendingRows.Next() {
			var pkg string
			var limit int
			var appliesAt time.Time
			pendingRows.Scan(&pkg, &limit, &appliesAt)

			a, ok := apps[pkg]
			if !ok {
				a = &AppRow{PackageName: pkg}
				apps[pkg] = a
			}
			a.PendingLimit = limit
			a.HasPending = true
			hours := int(math.Ceil(time.Until(appliesAt).Hours()))
			if hours < 1 {
				a.PendingIn = "soon"
			} else {
				a.PendingIn = fmt.Sprintf("%dh", hours)
			}
		}
	}

	// Load schedules
	scheduleRows, _ := wh.DB.Query("SELECT package_name, blocking_start_time, blocking_end_time FROM app_schedules WHERE user_uuid = ?", childUUID)
	if scheduleRows != nil {
		defer scheduleRows.Close()
		for scheduleRows.Next() {
			var pkg, start, end string
			scheduleRows.Scan(&pkg, &start, &end)
			if a, ok := apps[pkg]; ok {
				a.ScheduleStart = start
				a.ScheduleEnd = end
				a.HasSchedule = true
			}
		}
	}

	// Load today's usage
	usageRows, _ := wh.DB.Query("SELECT package_name, total_used_minutes, app_name FROM app_usage WHERE user_uuid = ? AND usage_date = ?", childUUID, today)
	if usageRows != nil {
		defer usageRows.Close()
		for usageRows.Next() {
			var pkg string
			var used int
			var appName sql.NullString
			usageRows.Scan(&pkg, &used, &appName)
			if a, ok := apps[pkg]; ok {
				a.TotalUsedMinutes = used
				a.UsedToday = true
				a.AppName = appName.String
			} else {
				apps[pkg] = &AppRow{PackageName: pkg, AppName: appName.String, TotalUsedMinutes: used, UsedToday: true}
			}
		}
	}

	// Split into limited, blocked, and other, sorted by usage desc
	var limited, blocked, other []AppRow
	for _, a := range apps {
		if a.AppName != "" {
			a.DisplayName = a.AppName
		} else {
			a.DisplayName = a.PackageName
		}
		// Resolve current blocking: global schedule takes priority, then per-app schedule
		if globalUnlockTime != "" {
			a.UnlockTime = globalUnlockTime
		} else if a.HasSchedule {
			a.UnlockTime = scheduleUnlockTime(a.ScheduleStart, a.ScheduleEnd)
		}
		if a.HasLimit && a.Blocked {
			blocked = append(blocked, *a)
		} else if a.HasLimit {
			a.Remaining = a.DailyLimitMinutes - a.TotalUsedMinutes
			a.OverLimit = a.Remaining <= 0
			limited = append(limited, *a)
		} else {
			other = append(other, *a)
		}
	}
	sort.Slice(limited, func(i, j int) bool { return limited[i].TotalUsedMinutes > limited[j].TotalUsedMinutes })
	sort.Slice(blocked, func(i, j int) bool { return blocked[i].DisplayName < blocked[j].DisplayName })
	sort.Slice(other, func(i, j int) bool { return other[i].TotalUsedMinutes > other[j].TotalUsedMinutes })

	var totalUsage int
	for _, a := range apps {
		totalUsage += a.TotalUsedMinutes
	}

	renderPartial(w, r, "child-apps", map[string]interface{}{
		"ChildUUID":        childUUID,
		"Limited":          limited,
		"Blocked":          blocked,
		"Other":            other,
		"TotalUsage":       totalUsage,
		"GlobalSchedule":   globalScheduleStr,
		"GlobalUnlockTime": globalUnlockTime,
	})
}
