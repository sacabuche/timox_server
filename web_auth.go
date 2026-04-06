package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math/rand"
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

		sub, role, err := validateSessionToken(cookie.Value)
		if err != nil || role != "parent" {
			clearSessionCookie(w)
			http.Redirect(w, r, "/web/login", http.StatusSeeOther)
			return
		}

		// Sliding session: renew the cookie if it expires within 15 days
		renewSessionIfExpiringSoon(w, cookie.Value, sub, role)

		ctx := withUserUUID(r.Context(), sub)
		ctx = withUserRole(ctx, role)
		ctx = context.WithValue(ctx, CtxLogger, slog.Default().With("user", sub))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// --- Public handlers ---

func (wh *WebHandler) showLogin(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session"); err == nil {
		if claims, err := parseJWT(cookie.Value); err == nil {
			if role, _ := claims["role"].(string); role == "parent" {
				http.Redirect(w, r, "/web/dashboard", http.StatusSeeOther)
				return
			}
		}
	}
	renderPage(w, r, "login-page", nil)
}

func (wh *WebHandler) showEmailForm(w http.ResponseWriter, r *http.Request) {
	renderPartial(w, r, "email-form", nil)
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
	if err != nil {
		log.Printf("[auth] login attempt for unknown email: %s\n", email)
		time.Sleep(time.Duration(200+rand.Intn(400)) * time.Millisecond)
	} else {
		token, err := generateToken()
		if err != nil {
			renderPartial(w, r, "login-error", map[string]interface{}{"Error": "Failed to generate token"})
			return
		}
		expiresAt := time.Now().Add(10 * time.Minute)
		wh.DB.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) VALUES (?, ?, ?)", token, userUUID, expiresAt)
		sendLoginToken(email, token)
	}

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

	persist := r.FormValue("stay_signed_in") == "1"
	setSessionCookie(w, jwtStr, persist)

	w.Header().Set("HX-Redirect", "/web/dashboard")
	w.WriteHeader(http.StatusOK)
}

func (wh *WebHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	http.Redirect(w, r, "/web/login", http.StatusSeeOther)
}

func setSessionCookie(w http.ResponseWriter, jwtStr string, persist bool) {
	cookie := &http.Cookie{
		Name:     "session",
		Value:    jwtStr,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	if persist {
		cookie.MaxAge = 30 * 24 * 60 * 60
		cookie.Expires = time.Now().Add(30 * 24 * time.Hour)
	}
	http.SetCookie(w, cookie)
}

func renewSessionIfExpiringSoon(w http.ResponseWriter, tokenStr, sub, role string) {
	claims, err := parseJWT(tokenStr)
	if err != nil {
		return
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		return
	}
	if time.Until(time.Unix(int64(exp), 0)) >= 15*24*time.Hour {
		return
	}
	newJWT, err := generateJWT(sub, role)
	if err != nil {
		return
	}
	setSessionCookie(w, newJWT, true)
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "session", MaxAge: -1, Path: "/"})
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
