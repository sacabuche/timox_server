package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type AppSchedule struct {
	BlockingStartTime string `json:"blockingStartTime"` // HH:MM
	BlockingEndTime   string `json:"blockingEndTime"`   // HH:MM
}

// Block type values for AppLimit.BlockType / app_limits.block_type.
const (
	BlockTypeNormal       = 0 // daily limit + global schedule apply
	BlockTypeBlocked      = 1 // always blocked, no exceptions
	BlockTypeGlobalExempt = 2 // exempt from global schedule; per-app limit still applies
	BlockTypeUnrestricted = 3 // never blocked, all limits ignored
)

type AppLimit struct {
	DailyLimitMinutes int           `json:"dailyLimitMinutes"`
	BlockType         int           `json:"blockType"`
	Schedule          []AppSchedule `json:"schedule,omitempty"`
}

// --- Context keys ---

type ContextKey string

const (
	CtxUserUUID ContextKey = "userUUID"
	CtxUserRole ContextKey = "userRole"
)

// --- Handler ---

type Handler struct {
	DB *sql.DB
}

func (h *Handler) parentOwnsChild(parentUUID, childUUID string) (bool, error) {
	var exists bool
	err := h.DB.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM parent_children WHERE parent_uuid = ? AND child_uuid = ?)",
		parentUUID, childUUID,
	).Scan(&exists)
	return exists, err
}

// Routes returns a chi.Router with all children endpoints mounted.
// The caller is responsible for applying auth + role middleware.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Delete("/{childUUID}", h.Delete)
	r.Post("/{childUUID}/token", h.GenerateToken)
	r.Get("/{childUUID}/usage", h.GetUsage)
	r.Get("/{childUUID}/app_limits", h.GetAppLimits)
	r.Post("/{childUUID}/app_limits/{packageName}", h.UpdateAppLimit)
	r.Delete("/{childUUID}/app_limits/{packageName}", h.DeleteAppLimit)

	return r
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)

	rows, err := h.DB.Query(
		`SELECT u.uuid, u.name, u.role FROM users u
		 INNER JOIN parent_children pc ON pc.child_uuid = u.uuid
		 WHERE pc.parent_uuid = ?`, parentUUID,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var list []User
	for rows.Next() {
		var u User
		var name sql.NullString
		if err := rows.Scan(&u.UUID, &name, &u.Role); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		u.Name = name.String
		list = append(list, u)
	}
	if list == nil {
		list = []User{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	childUUID := uuid.New().String()
	_, err := h.DB.Exec("INSERT INTO users (uuid, name, role) VALUES (?, ?, 'child')", childUUID, body.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_, err = h.DB.Exec("INSERT INTO parent_children (parent_uuid, child_uuid) VALUES (?, ?)", parentUUID, childUUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Auto-generate a login token for the child
	token, err := generateToken()
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}
	expiresAt := time.Now().Add(10 * time.Minute)
	_, err = h.DB.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) VALUES (?, ?, ?)", token, childUUID, expiresAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"uuid":      childUUID,
		"name":      body.Name,
		"role":      "child",
		"token":     token,
		"expiresAt": expiresAt.Format(time.RFC3339),
	})
}

func (h *Handler) GenerateToken(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	owns, err := h.parentOwnsChild(parentUUID, childUUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "child not found", http.StatusNotFound)
		return
	}

	token, err := generateToken()
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}
	expiresAt := time.Now().Add(10 * time.Minute)
	_, err = h.DB.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) VALUES (?, ?, ?)", token, childUUID, expiresAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"token":     token,
		"expiresAt": expiresAt.Format(time.RFC3339),
	})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	owns, err := h.parentOwnsChild(parentUUID, childUUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "child not found", http.StatusNotFound)
		return
	}

	_, err = h.DB.Exec("DELETE FROM users WHERE uuid = ?", childUUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GetAppLimits(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	owns, err := h.parentOwnsChild(parentUUID, childUUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "child not found", http.StatusNotFound)
		return
	}

	rows, err := h.DB.Query(
		`SELECT al.package_name, al.daily_limit_minutes, al.block_type,
		        s.blocking_start_time, s.blocking_end_time
		 FROM app_limits al
		 LEFT JOIN app_schedules s ON s.user_uuid = al.user_uuid AND s.package_name = al.package_name
		 WHERE al.user_uuid = ?`,
		childUUID,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	result := make(map[string]AppLimit)
	for rows.Next() {
		var pkg string
		var limit AppLimit
		var startTime, endTime sql.NullString
		if err := rows.Scan(&pkg, &limit.DailyLimitMinutes, &limit.BlockType, &startTime, &endTime); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if startTime.Valid && endTime.Valid {
			limit.Schedule = []AppSchedule{{BlockingStartTime: startTime.String, BlockingEndTime: endTime.String}}
		}
		result[pkg] = limit
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) UpdateAppLimit(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")
	packageName := chi.URLParam(r, "packageName")

	owns, err := h.parentOwnsChild(parentUUID, childUUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "child not found", http.StatusNotFound)
		return
	}

	var body struct {
		DailyLimitMinutes int `json:"dailyLimitMinutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Check if delayed changes is enabled and this is an increase
	delayed := isDelayedChangesEnabled(h.DB, childUUID)
	var currentLimit, currentBlockType int
	var hasExisting bool
	err = h.DB.QueryRow("SELECT daily_limit_minutes, block_type FROM app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName).Scan(&currentLimit, &currentBlockType)
	if err == nil {
		hasExisting = true
	}

	isIncrease := hasExisting && currentBlockType == BlockTypeNormal && body.DailyLimitMinutes > currentLimit

	if delayed && isIncrease {
		appliesAt := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02 15:04:05")
		_, err = h.DB.Exec(
			`INSERT INTO pending_app_limits (user_uuid, package_name, daily_limit_minutes, block_type, applies_at)
			 VALUES (?, ?, ?, 0, ?)
			 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes, block_type = excluded.block_type, applies_at = excluded.applies_at`,
			childUUID, packageName, body.DailyLimitMinutes, appliesAt,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Return current limit (unchanged) with pending info
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"dailyLimitMinutes": currentLimit,
			"blockType":         currentBlockType,
			"pending":           true,
			"pendingLimit":      body.DailyLimitMinutes,
			"appliesAt":         appliesAt,
		})
		return
	}

	_, err = h.DB.Exec(
		`INSERT INTO app_limits (user_uuid, package_name, daily_limit_minutes, block_type)
		 VALUES (?, ?, ?, 0)
		 ON CONFLICT(user_uuid, package_name) DO UPDATE SET daily_limit_minutes = excluded.daily_limit_minutes`,
		childUUID, packageName, body.DailyLimitMinutes,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Clear any pending limit
	h.DB.Exec("DELETE FROM pending_app_limits WHERE user_uuid = ? AND package_name = ?", childUUID, packageName)

	var limit AppLimit
	err = h.DB.QueryRow(
		"SELECT daily_limit_minutes, block_type FROM app_limits WHERE user_uuid = ? AND package_name = ?",
		childUUID, packageName,
	).Scan(&limit.DailyLimitMinutes, &limit.BlockType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(limit)
}

func (h *Handler) GetUsage(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")

	owns, err := h.parentOwnsChild(parentUUID, childUUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "child not found", http.StatusNotFound)
		return
	}

	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	rows, err := h.DB.Query(
		"SELECT package_name, total_used_minutes FROM app_usage WHERE user_uuid = ? AND usage_date = ? ORDER BY total_used_minutes DESC",
		childUUID, date,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type AppUsage struct {
		PackageName      string `json:"packageName"`
		TotalUsedMinutes int    `json:"totalUsedMinutes"`
	}

	var list []AppUsage
	for rows.Next() {
		var u AppUsage
		if err := rows.Scan(&u.PackageName, &u.TotalUsedMinutes); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		list = append(list, u)
	}
	if list == nil {
		list = []AppUsage{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *Handler) DeleteAppLimit(w http.ResponseWriter, r *http.Request) {
	parentUUID := r.Context().Value(CtxUserUUID).(string)
	childUUID := chi.URLParam(r, "childUUID")
	packageName := chi.URLParam(r, "packageName")

	owns, err := h.parentOwnsChild(parentUUID, childUUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "child not found", http.StatusNotFound)
		return
	}

	result, err := h.DB.Exec(
		"DELETE FROM app_limits WHERE user_uuid = ? AND package_name = ?",
		childUUID, packageName,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
