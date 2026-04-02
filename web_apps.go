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
		"SELECT package_name, daily_limit_minutes, block_type FROM pending_app_limits WHERE user_uuid = ? AND applies_at <= ?",
		childUUID, now,
	)
	if err != nil || rows == nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var pkg string
		var limit, blockType int
		rows.Scan(&pkg, &limit, &blockType)

		db.Exec(
			`INSERT INTO app_limits (user_uuid, package_name, daily_limit_minutes, block_type)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes, block_type = excluded.block_type`,
			childUUID, pkg, limit, blockType,
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

	var dailyLimit, blockType int
	var hasLimit bool
	err := wh.DB.QueryRow("SELECT daily_limit_minutes, block_type FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName).Scan(&dailyLimit, &blockType)
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
		"ChildUUID":            childUUID,
		"PackageName":          packageName,
		"DisplayName":          displayName,
		"DailyLimitMinutes":    dailyLimit,
		"HasLimit":             hasLimit,
		"BlockType":            blockType,
		"DelayedChanges":       delayed,
		"HasPending":           hasPending,
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
	blockTypeStr := r.FormValue("block_type")
	blockType, _ := strconv.Atoi(blockTypeStr)

	if packageName == "" || (blockType != BlockTypeBlocked && blockType != BlockTypeUnrestricted && dailyLimit == "") {
		http.Error(w, "package_name and daily_limit_minutes required", http.StatusBadRequest)
		return
	}

	// Auto-convert to Unrestricted when limit=0 and no schedule: no restrictions means unrestricted.
	// Applies to Normal and GlobalExempt (GlobalExempt with a schedule stays as-is).
	if blockType == BlockTypeNormal || blockType == BlockTypeGlobalExempt {
		newLimit, _ := strconv.Atoi(dailyLimit)
		if newLimit == 0 {
			schedStart := strings.TrimSpace(r.FormValue("schedule_start"))
			schedEnd := strings.TrimSpace(r.FormValue("schedule_end"))
			removeSchedule := r.FormValue("remove_schedule") == "1"
			// Check if a schedule will exist after this save
			var existingSchedStart string
			wh.DB.QueryRow("SELECT blocking_start_time FROM app_schedules WHERE user_uuid = ? AND package_name = ?", childUUID, packageName).Scan(&existingSchedStart)
			willHaveSchedule := (existingSchedStart != "" && !removeSchedule) || (schedStart != "" && schedEnd != "")
			if !willHaveSchedule {
				blockType = BlockTypeUnrestricted
			}
		}
	}

	if blockType == BlockTypeBlocked || blockType == BlockTypeUnrestricted {
		// block_type changes are always immediate — clear any pending increase
		if dailyLimit == "" {
			dailyLimit = "0"
		}
		wh.DB.Exec(
			`INSERT INTO app_limits (user_uuid, package_name, daily_limit_minutes, block_type)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes, block_type = excluded.block_type`,
			childUUID, packageName, dailyLimit, blockType,
		)
		wh.DB.Exec("DELETE FROM pending_app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName)
	} else {
		newLimit, _ := strconv.Atoi(dailyLimit)

		// Check if delayed changes is enabled and this is an increase
		delayed := isDelayedChangesEnabled(wh.DB, childUUID)
		var currentLimit, currentBlockType int
		var hasExisting bool
		err := wh.DB.QueryRow("SELECT daily_limit_minutes, block_type FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName).Scan(&currentLimit, &currentBlockType)
		if err == nil {
			hasExisting = true
		}

		isIncrease := hasExisting && currentBlockType == BlockTypeNormal && newLimit > currentLimit

		if delayed && isIncrease {
			// Store as pending with 24h delay
			appliesAt := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02 15:04:05")
			wh.DB.Exec(
				`INSERT INTO pending_app_limits (user_uuid, package_name, daily_limit_minutes, block_type, applies_at)
				 VALUES (?, ?, ?, ?, ?)
				 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes, block_type = excluded.block_type, applies_at = excluded.applies_at`,
				childUUID, packageName, newLimit, blockType, appliesAt,
			)
		} else {
			// Apply immediately (decrease, new limit, or delayed changes off)
			wh.DB.Exec(
				`INSERT INTO app_limits (user_uuid, package_name, daily_limit_minutes, block_type)
				 VALUES (?, ?, ?, ?)
				 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes, block_type = excluded.block_type`,
				childUUID, packageName, dailyLimit, blockType,
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
		BlockType         int
		Unlimited         bool // GlobalExempt + dailyLimitMinutes==0 → no daily limit
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

	todayStr := time.Now().Format("2006-01-02")
	selectedDate := r.URL.Query().Get("date")
	if selectedDate == "" {
		selectedDate = todayStr
	} else if _, err := time.Parse("2006-01-02", selectedDate); err != nil {
		selectedDate = todayStr
	} else if selectedDate > todayStr {
		selectedDate = todayStr
	}
	selTime, _ := time.Parse("2006-01-02", selectedDate)
	prevDate := selTime.AddDate(0, 0, -1).Format("2006-01-02")
	nextDate := selTime.AddDate(0, 0, 1).Format("2006-01-02")
	isToday := selectedDate == todayStr
	if nextDate > todayStr {
		nextDate = ""
	}
	today := selectedDate
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

	// Load limits, also fetch the most recent known app name from usage history
	limRows, _ := wh.DB.Query(`
		SELECT al.package_name, al.daily_limit_minutes, al.block_type,
		       (SELECT app_name FROM app_usage
		        WHERE user_uuid = al.user_uuid AND package_name = al.package_name AND app_name IS NOT NULL
		        ORDER BY usage_date DESC LIMIT 1)
		FROM app_limits al WHERE al.user_uuid = ? AND al.package_name != ?`, childUUID, GlobalTimePkg)
	if limRows != nil {
		defer limRows.Close()
		for limRows.Next() {
			a := &AppRow{}
			var appName sql.NullString
			limRows.Scan(&a.PackageName, &a.DailyLimitMinutes, &a.BlockType, &appName)
			a.AppName = appName.String
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

	// Load all ever-reported apps, with today's usage minutes (0 if not used today).
	usageRows, _ := wh.DB.Query(`
		SELECT
			au.package_name,
			COALESCE(MAX(CASE WHEN au.usage_date = ? THEN au.total_used_minutes END), 0) AS today_minutes,
			(SELECT app_name FROM app_usage
			 WHERE user_uuid = ? AND package_name = au.package_name AND app_name IS NOT NULL
			 ORDER BY usage_date DESC LIMIT 1) AS app_name
		FROM app_usage au
		WHERE au.user_uuid = ?
		GROUP BY au.package_name`, today, childUUID, childUUID)
	if usageRows != nil {
		defer usageRows.Close()
		for usageRows.Next() {
			var pkg string
			var used int
			var appName sql.NullString
			usageRows.Scan(&pkg, &used, &appName)
			usedToday := used > 0
			if a, ok := apps[pkg]; ok {
				a.TotalUsedMinutes = used
				a.UsedToday = usedToday
				if appName.String != "" {
					a.AppName = appName.String
				}
			} else {
				apps[pkg] = &AppRow{PackageName: pkg, AppName: appName.String, TotalUsedMinutes: used, UsedToday: usedToday}
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
		// Resolve schedule blocking — unrestricted is never blocked; global_exempt ignores global schedule only
		if a.HasLimit && a.BlockType == BlockTypeUnrestricted {
			// never blocked by any schedule
		} else if globalUnlockTime != "" && !(a.HasLimit && a.BlockType == BlockTypeGlobalExempt) {
			a.UnlockTime = globalUnlockTime
		} else if a.HasSchedule {
			a.UnlockTime = scheduleUnlockTime(a.ScheduleStart, a.ScheduleEnd)
		}
		if a.HasLimit && a.BlockType == BlockTypeBlocked {
			blocked = append(blocked, *a)
		} else if a.HasLimit {
			a.Unlimited = a.BlockType == BlockTypeGlobalExempt && a.DailyLimitMinutes == 0
			if !a.Unlimited {
				a.Remaining = a.DailyLimitMinutes - a.TotalUsedMinutes
				if a.BlockType != BlockTypeUnrestricted {
					a.OverLimit = a.Remaining <= 0
				}
			}
			limited = append(limited, *a)
		} else {
			other = append(other, *a)
		}
	}
	sort.Slice(limited, func(i, j int) bool {
		if limited[i].TotalUsedMinutes != limited[j].TotalUsedMinutes {
			return limited[i].TotalUsedMinutes > limited[j].TotalUsedMinutes
		}
		return limited[i].PackageName < limited[j].PackageName
	})
	sort.Slice(blocked, func(i, j int) bool { return blocked[i].DisplayName < blocked[j].DisplayName })
	sort.Slice(other, func(i, j int) bool {
		if other[i].TotalUsedMinutes != other[j].TotalUsedMinutes {
			return other[i].TotalUsedMinutes > other[j].TotalUsedMinutes
		}
		return other[i].PackageName < other[j].PackageName
	})

	var totalUsage int
	for _, a := range apps {
		totalUsage += a.TotalUsedMinutes
	}

	var totalLimitMinutes int
	var hasTotalLimit bool
	var rawTotal sql.NullInt64
	if err := wh.DB.QueryRow("SELECT daily_limit_minutes FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, GlobalTimePkg).Scan(&rawTotal); err == nil {
		totalLimitMinutes = int(rawTotal.Int64)
		hasTotalLimit = true
	}

	renderPartial(w, r, "child-apps", map[string]interface{}{
		"ChildUUID":         childUUID,
		"Limited":           limited,
		"Blocked":           blocked,
		"Other":             other,
		"TotalUsage":        totalUsage,
		"GlobalSchedule":    globalScheduleStr,
		"GlobalUnlockTime":  globalUnlockTime,
		"TotalLimitMinutes": totalLimitMinutes,
		"HasTotalLimit":     hasTotalLimit,
		"SelectedDate":      selectedDate,
		"TodayDate":         todayStr,
		"PrevDate":          prevDate,
		"NextDate":          nextDate,
		"IsToday":           isToday,
	})
}
