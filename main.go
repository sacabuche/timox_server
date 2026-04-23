package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"
)

var db *sql.DB

var jwtSecret []byte

var buildDate = "dev"
var commitSHA = "dev"

const refreshIntervalMs = 60000

var logger *slog.Logger

func initLogger() {
	logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
}

const CtxLogger ctxKey = "logger"

func logFromCtx(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(CtxLogger).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// --- Database setup ---

func setupDB(dsn string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	_, err = d.Exec(`PRAGMA foreign_keys = ON`)
	if err != nil {
		return nil, err
	}

	_, err = d.Exec(`PRAGMA journal_mode=WAL`)
	if err != nil {
		return nil, err
	}

	_, err = d.Exec(`PRAGMA synchronous=NORMAL`)
	if err != nil {
		return nil, err
	}

	if err := runMigrations(d); err != nil {
		return nil, err
	}

	return d, nil
}

func initDB() {
	dsn := cfg.DBPath
	var err error
	db, err = setupDB(dsn)
	if err != nil {
		log.Fatal(err)
	}
}

// --- JWT helpers ---

func generateJWT(userUUID, role string) (string, error) {
	claims := jwt.MapClaims{
		"sub":  userUUID,
		"role": role,
		"exp":  time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func generateToken() (string, error) {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return "", err
		}
		b[i] = letters[n.Int64()]
	}
	return string(b), nil
}

// --- Middleware ---

// validateSessionToken parses a JWT string, extracts sub+role, and verifies the user exists in DB.
func validateSessionToken(tokenStr string) (sub, role string, err error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return "", "", fmt.Errorf("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", fmt.Errorf("invalid claims")
	}

	sub, _ = claims.GetSubject()
	role, _ = claims["role"].(string)
	if sub == "" || role == "" {
		return "", "", fmt.Errorf("invalid token claims")
	}

	var dbRole string
	if err = db.QueryRow("SELECT role FROM users WHERE uuid = ?", sub).Scan(&dbRole); err != nil || dbRole != role {
		return "", "", fmt.Errorf("user not found")
	}

	return sub, role, nil
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}

		sub, role, err := validateSessionToken(strings.TrimPrefix(auth, "Bearer "))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), CtxUserUUID, sub)
		ctx = context.WithValue(ctx, CtxUserRole, role)
		ctx = context.WithValue(ctx, CtxLogger, slog.Default().With("user", sub))
		logFromCtx(ctx).Info("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userRole := r.Context().Value(CtxUserRole).(string)
			if userRole != role {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- Public handlers ---

func handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
		http.Error(w, "valid email is required", http.StatusBadRequest)
		return
	}

	newUUID := uuid.New().String()
	_, err := db.Exec("INSERT INTO users (uuid, email, role) VALUES (?, ?, 'parent')", newUUID, body.Email)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			http.Error(w, "email already exists", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user := User{UUID: newUUID, Email: body.Email, Role: "parent"}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

func handleAuthToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
		http.Error(w, "valid email is required", http.StatusBadRequest)
		return
	}

	var user User
	err := db.QueryRow("SELECT uuid, email, role FROM users WHERE email = ?", body.Email).Scan(&user.UUID, &user.Email, &user.Role)
	if err == sql.ErrNoRows {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	token, err := generateToken()
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(10 * time.Minute)
	_, err = db.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) VALUES (?, ?, ?)", token, user.UUID, expiresAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sendLoginToken(user.Email, token)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"token":     token,
		"expiresAt": expiresAt.Format(time.RFC3339),
	})
}

func handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" || body.Token == "" {
		http.Error(w, "email and token are required", http.StatusBadRequest)
		return
	}

	var user User
	err := db.QueryRow("SELECT uuid, email, role FROM users WHERE email = ?", body.Email).Scan(&user.UUID, &user.Email, &user.Role)
	if err == sql.ErrNoRows {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var expiresAt time.Time
	err = db.QueryRow("SELECT expires_at FROM auth_tokens WHERE token = ? AND user_uuid = ?", body.Token, user.UUID).Scan(&expiresAt)
	if err == sql.ErrNoRows {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Delete the token (one-time use)
	db.Exec("DELETE FROM auth_tokens WHERE token = ?", body.Token)

	if time.Now().After(expiresAt) {
		http.Error(w, "token expired", http.StatusUnauthorized)
		return
	}

	jwtToken, err := generateJWT(user.UUID, user.Role)
	if err != nil {
		http.Error(w, "failed to generate JWT", http.StatusInternalServerError)
		return
	}

	slog.Info("user logged in", "user", user.UUID, "role", user.Role)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"jwt": jwtToken,
	})
}

func handleChildLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	var userUUID, role string
	var userName sql.NullString
	var expiresAt time.Time
	err := db.QueryRow(
		`SELECT u.uuid, u.role, u.name, at.expires_at FROM auth_tokens at
		 JOIN users u ON u.uuid = at.user_uuid
		 WHERE at.token = ?`, body.Token,
	).Scan(&userUUID, &role, &userName, &expiresAt)
	if err == sql.ErrNoRows {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Delete the token (one-time use)
	db.Exec("DELETE FROM auth_tokens WHERE token = ?", body.Token)

	if time.Now().After(expiresAt) {
		http.Error(w, "token expired", http.StatusUnauthorized)
		return
	}

	jwtToken, err := generateJWT(userUUID, role)
	if err != nil {
		http.Error(w, "failed to generate JWT", http.StatusInternalServerError)
		return
	}

	slog.Info("user logged in", "user", userUUID, "role", role)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"jwt":  jwtToken,
		"name": userName.String,
	})
}

// --- Profile ---

func handleMe(w http.ResponseWriter, r *http.Request) {
	userUUID := r.Context().Value(CtxUserUUID).(string)

	var name sql.NullString
	var role string
	err := db.QueryRow("SELECT name, role FROM users WHERE uuid = ?", userUUID).Scan(&name, &role)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"uuid": userUUID,
		"name": name.String,
		"role": role,
	})
}

// --- Child (own limits) ---

func handleChildAppLimits(w http.ResponseWriter, r *http.Request) {
	userUUID := r.Context().Value(CtxUserUUID).(string)
	logFromCtx(r.Context()).Info("get app limits")

	// Apply any pending limits that are past due
	applyPendingLimits(db, userUUID)

	// Load global blocking schedule
	var globalStart, globalEnd sql.NullString
	db.QueryRow("SELECT blocking_start_time, blocking_end_time FROM users WHERE uuid = ?", userUUID).Scan(&globalStart, &globalEnd)

	var globalSchedule []AppSchedule
	if globalStart.Valid && globalEnd.Valid {
		globalSchedule = []AppSchedule{{BlockingStartTime: globalStart.String, BlockingEndTime: globalEnd.String}}
	}

	rows, err := db.Query(
		`SELECT al.package_name, al.daily_limit_minutes, al.block_type,
		        s.blocking_start_time, s.blocking_end_time
		 FROM app_limits al
		 LEFT JOIN app_schedules s ON s.user_uuid = al.user_uuid AND s.package_name = al.package_name
		 WHERE al.user_uuid = ?`,
		userUUID,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	limits := make(map[string]AppLimit)
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
		limits[pkg] = limit
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"globalSchedule": globalSchedule,
		"limits":         limits,
	})
}

func handleChildLimitsVersion(w http.ResponseWriter, r *http.Request) {
	userUUID := r.Context().Value(CtxUserUUID).(string)
	logFromCtx(r.Context()).Info("check limits version")

	var updatedAt sql.NullString
	if err := db.QueryRow("SELECT limits_updated_at FROM users WHERE uuid = ?", userUUID).Scan(&updatedAt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if updatedAt.Valid {
		json.NewEncoder(w).Encode(map[string]string{"updatedAt": updatedAt.String})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"updatedAt": nil})
}

type reportEntry struct {
	PackageName      string `json:"packageName"`
	AppName          string `json:"appName"`
	TotalUsedMinutes int    `json:"totalUsedMinutes"`
}

// parseReportBody accepts both the legacy bare-array format and the new
// envelope format {"timestamp":"<RFC3339>","apps":[...]}.
// It returns the entries, the usage date to store (YYYY-MM-DD in the device's
// local timezone when a timestamp is present, UTC date otherwise), a pointer to
// the clock skew in minutes (when a timestamp was supplied), and a pointer to
// the minute-of-day (0–1439) derived from the device timestamp (or server time).
func parseReportBody(body []byte) (entries []reportEntry, usageDate string, skew *int, minuteOfDay *int, err error) {
	now := time.Now().UTC()
	usageDate = now.Format("2006-01-02")
	mod := now.Hour()*60 + now.Minute()
	minuteOfDay = &mod

	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 {
		return nil, "", nil, nil, fmt.Errorf("empty body")
	}

	if trimmed[0] != '{' {
		// Legacy format: bare JSON array.
		if err = json.Unmarshal(body, &entries); err != nil {
			return nil, "", nil, nil, fmt.Errorf("invalid JSON body, expected array or {timestamp,apps} object")
		}
		return entries, usageDate, nil, minuteOfDay, nil
	}

	// New envelope format.
	var envelope struct {
		Timestamp string        `json:"timestamp"`
		Apps      []reportEntry `json:"apps"`
	}
	if err = json.Unmarshal(body, &envelope); err != nil {
		return nil, "", nil, nil, fmt.Errorf("invalid JSON body")
	}
	entries = envelope.Apps

	if envelope.Timestamp == "" {
		return entries, usageDate, nil, minuteOfDay, nil
	}

	deviceTime, parseErr := time.Parse(time.RFC3339Nano, envelope.Timestamp)
	if parseErr != nil {
		deviceTime, parseErr = time.Parse(time.RFC3339, envelope.Timestamp)
	}
	if parseErr != nil {
		// Unparseable timestamp — fall back to server date, still accept the report.
		return entries, usageDate, nil, minuteOfDay, nil
	}

	// RFC3339 embeds the timezone offset, so Format gives the date in the device's local tz.
	usageDate = deviceTime.Format("2006-01-02")
	skewMin := int(deviceTime.Sub(time.Now().UTC()).Minutes())
	devMOD := deviceTime.Hour()*60 + deviceTime.Minute()
	return entries, usageDate, &skewMin, &devMOD, nil
}

func handleReportAppUsage(w http.ResponseWriter, r *http.Request) {
	userUUID := r.Context().Value(CtxUserUUID).(string)
	log := logFromCtx(r.Context())
	log.Info("received usage report")

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	log.Info("report body", "body", string(bodyBytes))
	apps, usageDate, skewMinutes, minuteOfDay, err := parseReportBody(bodyBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	db.Exec("UPDATE users SET last_report_at = CURRENT_TIMESTAMP WHERE uuid = ?", userUUID)
	if skewMinutes != nil {
		db.Exec("UPDATE users SET last_report_skew_minutes = ? WHERE uuid = ?", *skewMinutes, userUUID)
		log.Info("device timestamp received", "skewMinutes", *skewMinutes)
	}

	if len(apps) == 0 {
		http.Error(w, "empty report", http.StatusBadRequest)
		return
	}

	log.Info("usage report entries", "count", len(apps), "usageDate", usageDate)
	for _, entry := range apps {
		log.Info("app usage", "package", entry.PackageName, "app", entry.AppName, "minutes", entry.TotalUsedMinutes)
		if entry.PackageName == "" {
			http.Error(w, "packageName is required for each entry", http.StatusBadRequest)
			return
		}
		if entry.TotalUsedMinutes < 0 {
			http.Error(w, "totalUsedMinutes cannot be negative", http.StatusBadRequest)
			return
		}

		mod := *minuteOfDay
		_, err := db.Exec(
			`INSERT INTO app_usage (user_uuid, package_name, usage_date, total_used_minutes, app_name, snapshots)
			 VALUES (?, ?, ?, ?, ?, json_array(json_array(?, ?)))
			 ON CONFLICT(user_uuid, package_name, usage_date) DO UPDATE SET
			   total_used_minutes = excluded.total_used_minutes,
			   app_name           = COALESCE(excluded.app_name, app_usage.app_name),
			   snapshots          = json_insert(app_usage.snapshots, '$[#]', json_array(?, ?))`,
			userUUID, entry.PackageName, usageDate, entry.TotalUsedMinutes, nilIfEmpty(entry.AppName),
			mod, entry.TotalUsedMinutes,
			mod, entry.TotalUsedMinutes,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// --- Router ---

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Compress(5))

	// Public
	r.Post("/users", handleCreateUser)
	r.Post("/auth/token", handleAuthToken)
	r.Post("/auth/login", handleAuthLogin)
	r.Post("/auth/child-login", handleChildLogin)

	// Authenticated: get own profile
	r.With(authMiddleware).Get("/me", handleMe)

	// Child: own limits (JWT required, role=child)
	r.With(authMiddleware, requireRole("child")).Get("/app_limits", handleChildAppLimits)
	r.With(authMiddleware, requireRole("child")).Get("/limits_version", handleChildLimitsVersion)

	// Parent: children management (JWT required, role=parent)
	childrenHandler := &Handler{DB: db}
	r.With(authMiddleware, requireRole("parent")).Mount("/children", childrenHandler.Routes())

	// Child: Report app usage (JWT required, role=child)
	r.With(authMiddleware, requireRole("child")).Post("/report", handleReportAppUsage)
	r.With(authMiddleware, requireRole("child")).Post("/events", handleReportEvents)

	// Icons
	r.With(authMiddleware, requireRole("child")).Post("/icons/check", handleIconsCheck)
	r.With(authMiddleware, requireRole("child")).Post("/icons", handleIconUpload)
	r.Get("/apps/{packages}", handleGetApps)

	// Static file serving (dev only; in prod Caddy serves /static/)
	if cfg.ServeStatic {
		iconsDir := http.Dir(cfg.IconsDir)
		r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(iconsDir)))
	}

	// Web UI (parent dashboard)
	webHandler := &WebHandler{DB: db}
	r.Mount("/web", webHandler.Routes())

	// Redirect root to web dashboard
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
	})

	return r
}

func main() {
	initLogger()

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found, using environment variables")
	}

	loadConfig()
	jwtSecret = []byte(cfg.JWTSecret)

	initDB()
	defer db.Close()

	r := newRouter()
	slog.Info("server listening", "addr", ":8080", "build", buildDate, "commit", commitSHA)
	log.Fatal(http.ListenAndServe(":8080", r))
}
