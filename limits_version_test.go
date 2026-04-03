package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type testFixture struct {
	parentJWT string
	childUUID string
	childJWT  string
}

// setupParentAndChild creates a parent, logs them in, creates a child, and logs the child in.
func setupParentAndChild(t *testing.T, handler http.Handler) testFixture {
	t.Helper()
	createParent(t, handler, "parent@test.com")
	parentJWT := loginParent(t, handler, "parent@test.com")
	childResp := createChildViaParent(t, handler, parentJWT, "kiddo")
	return testFixture{
		parentJWT: parentJWT,
		childUUID: childResp["uuid"].(string),
		childJWT:  loginChild(t, handler, childResp["token"].(string)),
	}
}

// getLimitsUpdatedAt reads limits_updated_at directly from the DB.
func getLimitsUpdatedAt(t *testing.T, childUUID string) *string {
	t.Helper()
	var v sql.NullString
	db.QueryRow("SELECT limits_updated_at FROM users WHERE uuid = ?", childUUID).Scan(&v)
	if v.Valid {
		return &v.String
	}
	return nil
}

// getLimitsVersionAPI calls GET /limits_version as the child and returns updatedAt or nil.
func getLimitsVersionAPI(t *testing.T, handler http.Handler, childJWT string) *string {
	t.Helper()
	req := authReq(http.MethodGet, "/limits_version", "", childJWT)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /limits_version: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if v, ok := resp["updatedAt"]; ok && v != nil {
		s := v.(string)
		return &s
	}
	return nil
}

// setLimitViaAPI sets a daily limit for a package via the parent API and returns the status code.
func setLimitViaAPI(t *testing.T, handler http.Handler, parentJWT, childUUID, pkg string, minutes int) int {
	t.Helper()
	body := fmt.Sprintf(`{"dailyLimitMinutes":%d}`, minutes)
	req := authReq(http.MethodPost, "/children/"+childUUID+"/app_limits/"+pkg, body, parentJWT)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

// webLoginSession inserts a token directly and returns a session cookie.
func webLoginSession(t *testing.T, handler http.Handler, email string) *http.Cookie {
	t.Helper()
	var token string
	err := db.QueryRow(
		"INSERT INTO auth_tokens (token, user_uuid, expires_at) SELECT 'WLSTOKEN', uuid, datetime('now', '+10 minutes') FROM users WHERE email = ? RETURNING token",
		email,
	).Scan(&token)
	if err != nil {
		t.Fatalf("insert web login token: %v", err)
	}
	return getSessionCookie(t, handler, email, token)
}

// ===================== GET /limits_version tests =====================

func TestLimitsVersion_NullInitially(t *testing.T) {
	setupTestDB(t)
	r := newRouter()
	f := setupParentAndChild(t, r)

	v := getLimitsVersionAPI(t, r, f.childJWT)
	if v != nil {
		t.Errorf("expected null updatedAt for fresh child, got %q", *v)
	}
}

func TestLimitsVersion_RequiresChildRole(t *testing.T) {
	setupTestDB(t)
	r := newRouter()
	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")

	req := authReq(http.MethodGet, "/limits_version", "", parentJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for parent accessing /limits_version, got %d", rec.Code)
	}
}

func TestLimitsVersion_UpdatedAfterSetLimit(t *testing.T) {
	setupTestDB(t)
	r := newRouter()
	f := setupParentAndChild(t, r)

	code := setLimitViaAPI(t, r, f.parentJWT, f.childUUID, "com.example.app", 60)
	if code != http.StatusOK {
		t.Fatalf("set limit: expected 200, got %d", code)
	}

	if getLimitsVersionAPI(t, r, f.childJWT) == nil {
		t.Error("expected non-null updatedAt after setting a limit")
	}
}

func TestLimitsVersion_UpdatedAfterDeleteLimit(t *testing.T) {
	setupTestDB(t)
	r := newRouter()
	f := setupParentAndChild(t, r)

	setLimitViaAPI(t, r, f.parentJWT, f.childUUID, "com.example.app", 60)
	if getLimitsUpdatedAt(t, f.childUUID) == nil {
		t.Fatal("expected updatedAt to be set after initial limit")
	}

	// Roll back the timestamp so the next touch produces a distinct value, then read the rolled-back value
	db.Exec("UPDATE users SET limits_updated_at = datetime(limits_updated_at, '-1 second') WHERE uuid = ?", f.childUUID)
	first := getLimitsUpdatedAt(t, f.childUUID)

	req := authReq(http.MethodDelete, "/children/"+f.childUUID+"/app_limits/com.example.app", "", f.parentJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete limit: expected 204, got %d", rec.Code)
	}

	second := getLimitsUpdatedAt(t, f.childUUID)
	if second == nil {
		t.Fatal("expected updatedAt to still be set after delete")
	}
	if *first == *second {
		t.Error("expected updatedAt to change after deleting limit")
	}
}

func TestLimitsVersion_PendingIncreaseDoesNotUpdate(t *testing.T) {
	setupTestDB(t)
	r := newRouter()
	f := setupParentAndChild(t, r)

	// Set initial limit (immediate — delayed_changes not yet enabled)
	setLimitViaAPI(t, r, f.parentJWT, f.childUUID, "com.example.app", 60)
	before := getLimitsUpdatedAt(t, f.childUUID)
	if before == nil {
		t.Fatal("expected updatedAt after initial set")
	}

	// Enable delayed changes
	db.Exec("UPDATE users SET delayed_changes = true WHERE uuid = ?", f.childUUID)

	// Attempt an increase — should queue as pending, not apply immediately
	req := authReq(http.MethodPost, "/children/"+f.childUUID+"/app_limits/com.example.app", `{"dailyLimitMinutes":120}`, f.parentJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["pending"] != true {
		t.Fatalf("expected pending=true in response, got %v", resp)
	}

	after := getLimitsUpdatedAt(t, f.childUUID)
	if after == nil {
		t.Fatal("expected updatedAt to still be set")
	}
	if *before != *after {
		t.Errorf("expected updatedAt unchanged after pending increase, before=%q after=%q", *before, *after)
	}
}

func TestLimitsVersion_UpdatedWhenPendingLimitApplies(t *testing.T) {
	setupTestDB(t)
	r := newRouter()
	f := setupParentAndChild(t, r)

	// Set initial limit, then enable delayed changes and queue an increase
	setLimitViaAPI(t, r, f.parentJWT, f.childUUID, "com.example.app", 60)
	db.Exec("UPDATE users SET delayed_changes = true WHERE uuid = ?", f.childUUID)
	setLimitViaAPI(t, r, f.parentJWT, f.childUUID, "com.example.app", 120)

	var count int
	db.QueryRow("SELECT COUNT(*) FROM pending_app_limits WHERE user_uuid = ?", f.childUUID).Scan(&count)
	if count == 0 {
		t.Fatal("expected a pending limit to be queued")
	}

	// Wind the clock: make the pending limit past-due, and roll back limits_updated_at so the
	// touch from applyPendingLimits produces a distinct value
	db.Exec("UPDATE pending_app_limits SET applies_at = datetime('now', '-1 second') WHERE user_uuid = ?", f.childUUID)
	db.Exec("UPDATE users SET limits_updated_at = datetime(limits_updated_at, '-1 second') WHERE uuid = ?", f.childUUID)
	before := getLimitsUpdatedAt(t, f.childUUID)

	// GET /app_limits triggers applyPendingLimits
	req := authReq(http.MethodGet, "/app_limits", "", f.childJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app_limits: expected 200, got %d", rec.Code)
	}

	after := getLimitsUpdatedAt(t, f.childUUID)
	if after == nil {
		t.Fatal("expected updatedAt to be set after pending limit applied")
	}
	if before != nil && *before == *after {
		t.Error("expected updatedAt to change when pending limit was applied")
	}

	db.QueryRow("SELECT COUNT(*) FROM pending_app_limits WHERE user_uuid = ?", f.childUUID).Scan(&count)
	if count != 0 {
		t.Error("expected pending limit to be cleared after apply")
	}
}

func TestLimitsVersion_UpdatedAfterGlobalSchedule(t *testing.T) {
	setupTestDB(t)
	r := newRouter()
	f := setupParentAndChild(t, r)

	cookie := webLoginSession(t, r, "parent@test.com")

	form := url.Values{}
	form.Set("schedule_start", "22:00")
	form.Set("schedule_end", "07:00")
	req := webReq(http.MethodPost, "/web/children/"+f.childUUID+"/global-schedule", form, cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set global schedule: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if getLimitsVersionAPI(t, r, f.childJWT) == nil {
		t.Error("expected non-null updatedAt after setting global schedule")
	}
}
