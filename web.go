package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-chi/chi/v5"
)

// --- Context helpers ---

func withUserUUID(ctx context.Context, uuid string) context.Context {
	return context.WithValue(ctx, CtxUserUUID, uuid)
}

func withUserRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, CtxUserRole, role)
}

// --- Web handler ---

type WebHandler struct {
	DB *sql.DB
}

func (wh *WebHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(languageMiddleware)

	// Public
	r.Get("/login", wh.showLogin)
	r.Post("/login", wh.handleLogin)
	r.Get("/register", wh.showRegister)
	r.Post("/register", wh.handleRegister)
	r.Post("/request-token", wh.handleRequestToken)
	r.Get("/logout", wh.handleLogout)

	// Authenticated
	r.Group(func(r chi.Router) {
		r.Use(webAuthMiddleware)
		r.Get("/dashboard", wh.showDashboard)
		r.Post("/children", wh.createChild)
		r.Get("/children/{childUUID}", wh.showChild)
		r.Get("/children/{childUUID}/settings", wh.showChildSettings)
		r.Get("/children/{childUUID}/welcome", wh.showChildWelcome)
		r.Get("/children/{childUUID}/welcome-status", wh.checkWelcomeStatus)
		r.Delete("/children/{childUUID}", wh.deleteChild)
		r.Post("/children/{childUUID}/token", wh.generateChildToken)
		r.Get("/children/{childUUID}/token-status", wh.checkChildTokenStatus)
		r.Get("/children/{childUUID}/apps", wh.getChildApps)
		r.Get("/children/{childUUID}/limits/{packageName}/edit", wh.editChildLimit)
		r.Get("/children/{childUUID}/limits/{packageName}/confirm-delete", wh.confirmDeleteChildLimit)
		r.Post("/children/{childUUID}/limits", wh.addChildLimit)
		r.Delete("/children/{childUUID}/limits/{packageName}", wh.deleteChildLimit)
		r.Delete("/children/{childUUID}/schedules/{packageName}", wh.deleteChildSchedule)
		r.Post("/children/{childUUID}/global-schedule", wh.saveChildGlobalSchedule)
		r.Post("/children/{childUUID}/delayed-changes", wh.toggleDelayedChanges)
	})

	return r
}

// --- Helpers ---

// isValidTime checks that s is a valid HH:MM time string.
func isValidTime(s string) bool {
	_, err := time.Parse("15:04", s)
	return err == nil
}

// scheduleBlockedDuration returns the duration the app is blocked given a
// blocking window from start to end (both HH:MM), e.g. "20:00"–"23:00" → "3h".
func scheduleBlockedDuration(start, end string) string {
	s, err1 := time.Parse("15:04", start)
	e, err2 := time.Parse("15:04", end)
	if err1 != nil || err2 != nil {
		return ""
	}
	blockedMin := int(e.Sub(s).Minutes())
	if blockedMin < 0 {
		blockedMin += 24 * 60
	}
	h := blockedMin / 60
	m := blockedMin % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dmin", h, m)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	m := int(d.Minutes())
	if m == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}
