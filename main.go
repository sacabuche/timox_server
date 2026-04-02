package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
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

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("New request")
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}
		tokenStr := strings.TrimPrefix(auth, "Bearer ")

		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return jwtSecret, nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, "invalid claims", http.StatusUnauthorized)
			return
		}

		sub, _ := claims.GetSubject()
		role, _ := claims["role"].(string)
		if sub == "" || role == "" {
			http.Error(w, "invalid token claims", http.StatusUnauthorized)
			return
		}

		// Verify user still exists in DB
		var dbRole string
		err = db.QueryRow("SELECT role FROM users WHERE uuid = ?", sub).Scan(&dbRole)
		if err != nil || dbRole != role {
			http.Error(w, "user not found", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), CtxUserUUID, sub)
		ctx = context.WithValue(ctx, CtxUserRole, role)
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

func handleReportAppUsage(w http.ResponseWriter, r *http.Request) {
	userUUID := r.Context().Value(CtxUserUUID).(string)
	fmt.Printf("Received app usage report from user %s\n", userUUID)
	var body []struct {
		PackageName      string `json:"packageName"`
		AppName          string `json:"appName"`
		TotalUsedMinutes int    `json:"totalUsedMinutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body, expected array", http.StatusBadRequest)
		return
	}

	if len(body) == 0 {
		http.Error(w, "empty report array", http.StatusBadRequest)
		return
	}

	today := time.Now().Format("2006-01-02")

	fmt.Printf("Received usage report from user: %+v\n\n", body)
	for _, entry := range body {
		if entry.PackageName == "" {
			http.Error(w, "packageName is required for each entry", http.StatusBadRequest)
			return
		}
		if entry.TotalUsedMinutes < 0 {
			http.Error(w, "totalUsedMinutes cannot be negative", http.StatusBadRequest)
			return
		}

		_, err := db.Exec(
			`INSERT INTO app_usage (user_uuid, package_name, usage_date, total_used_minutes, app_name)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(user_uuid, package_name, usage_date) DO UPDATE SET
			   total_used_minutes = MAX(app_usage.total_used_minutes, excluded.total_used_minutes),
			   app_name = COALESCE(excluded.app_name, app_usage.app_name)`,
			userUUID, entry.PackageName, today, entry.TotalUsedMinutes, nilIfEmpty(entry.AppName),
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

	// Parent: children management (JWT required, role=parent)
	childrenHandler := &Handler{DB: db}
	r.With(authMiddleware, requireRole("parent")).Mount("/children", childrenHandler.Routes())

	// Child: Report app usage (JWT required, role=child)
	r.With(authMiddleware, requireRole("child")).Post("/report", handleReportAppUsage)

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
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	loadConfig()
	jwtSecret = []byte(cfg.JWTSecret)

	initDB()
	defer db.Close()

	r := newRouter()
	fmt.Println("Server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}
