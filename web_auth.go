package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// --- Cookie auth middleware ---

func webAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, "/web/login", http.StatusSeeOther)
			return
		}

		claims, err := parseJWT(cookie.Value)
		if err != nil {
			http.SetCookie(w, &http.Cookie{Name: "session", MaxAge: -1, Path: "/"})
			http.Redirect(w, r, "/web/login", http.StatusSeeOther)
			return
		}

		sub, _ := claims["sub"].(string)
		role, _ := claims["role"].(string)
		if sub == "" || role != "parent" {
			http.Redirect(w, r, "/web/login", http.StatusSeeOther)
			return
		}

		// Verify user still exists in DB
		var dbRole string
		err = db.QueryRow("SELECT role FROM users WHERE uuid = ?", sub).Scan(&dbRole)
		if err != nil || dbRole != role {
			http.SetCookie(w, &http.Cookie{Name: "session", MaxAge: -1, Path: "/"})
			http.Redirect(w, r, "/web/login", http.StatusSeeOther)
			return
		}

		ctx := withUserUUID(r.Context(), sub)
		ctx = withUserRole(ctx, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// --- Public handlers ---

func (wh *WebHandler) showLogin(w http.ResponseWriter, r *http.Request) {
	renderPage(w, r, "login-page", nil)
}

func (wh *WebHandler) showRegister(w http.ResponseWriter, r *http.Request) {
	renderPage(w, r, "register-page", map[string]interface{}{})
}

func (wh *WebHandler) handleRegister(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		renderPage(w, r, "register-page", map[string]interface{}{"Error": "Email is required"})
		return
	}

	newUUID := uuid.New().String()
	_, err := wh.DB.Exec("INSERT INTO users (uuid, email, role) VALUES (?, ?, 'parent')", newUUID, email)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			renderPage(w, r, "register-page", map[string]interface{}{"Error": "Email already exists"})
			return
		}
		renderPage(w, r, "register-page", map[string]interface{}{"Error": "Server error"})
		return
	}

	renderPage(w, r, "register-page", map[string]interface{}{"Success": true})
}

func (wh *WebHandler) handleRequestToken(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		renderPartial(w, r, "login-error", map[string]interface{}{"Error": "Email is required"})
		return
	}

	var userUUID string
	err := wh.DB.QueryRow("SELECT uuid FROM users WHERE email = ? AND role = 'parent'", email).Scan(&userUUID)
	if err == sql.ErrNoRows {
		renderPartial(w, r, "login-error", map[string]interface{}{"Error": "User not found"})
		return
	} else if err != nil {
		renderPartial(w, r, "login-error", map[string]interface{}{"Error": "Server error"})
		return
	}

	token, err := generateToken()
	if err != nil {
		renderPartial(w, r, "login-error", map[string]interface{}{"Error": "Failed to generate token"})
		return
	}

	expiresAt := time.Now().Add(10 * time.Minute)
	wh.DB.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) VALUES (?, ?, ?)", token, userUUID, expiresAt)

	fmt.Printf("[Web] Auth token for %s: %s\n", email, token)
	renderPartial(w, r, "token-step", map[string]interface{}{"Email": email})
}

func (wh *WebHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	token := strings.TrimSpace(strings.ToUpper(r.FormValue("token")))

	if email == "" || token == "" {
		renderPartial(w, r, "login-error", map[string]interface{}{"Error": "Email and token are required"})
		return
	}

	var userUUID, role string
	err := wh.DB.QueryRow("SELECT uuid, role FROM users WHERE email = ?", email).Scan(&userUUID, &role)
	if err != nil {
		renderPartial(w, r, "login-error", map[string]interface{}{"Error": "Invalid credentials"})
		return
	}

	var expiresAt time.Time
	err = wh.DB.QueryRow("SELECT expires_at FROM auth_tokens WHERE token = ? AND user_uuid = ?", token, userUUID).Scan(&expiresAt)
	if err != nil {
		renderPartial(w, r, "login-error", map[string]interface{}{"Error": "Invalid credentials"})
		return
	}

	wh.DB.Exec("DELETE FROM auth_tokens WHERE token = ?", token)

	if time.Now().After(expiresAt) {
		renderPartial(w, r, "login-error", map[string]interface{}{"Error": "Token expired"})
		return
	}

	jwtStr, err := generateJWT(userUUID, role)
	if err != nil {
		renderPartial(w, r, "login-error", map[string]interface{}{"Error": "Server error"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    jwtStr,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 60 * 60,
	})

	w.Header().Set("HX-Redirect", "/web/dashboard")
}

func (wh *WebHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "session", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/web/login", http.StatusSeeOther)
}

// parseJWT validates a JWT string and returns the claims
func parseJWT(tokenStr string) (map[string]interface{}, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims")
	}
	return claims, nil
}
