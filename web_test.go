package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func webReq(method, path string, body url.Values, sessionCookie *http.Cookie) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(body.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if sessionCookie != nil {
		req.AddCookie(sessionCookie)
	}
	return req
}

func getSessionCookie(t *testing.T, handler http.Handler, email, token string) *http.Cookie {
	t.Helper()
	form := url.Values{}
	form.Add("email", email)
	form.Add("token", token)
	req := webReq(http.MethodPost, "/web/login", form, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "session" {
			return cookie
		}
	}
	t.Fatal("session cookie not found in response")
	return nil
}

func TestWeb_LoginFlow(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	email := "webparent@test.com"
	createParent(t, r, email)

	// 1. Request token
	form := url.Values{}
	form.Add("email", email)
	req := webReq(http.MethodPost, "/web/request-token", form, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("request-token: expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "token") {
		t.Error("expected response to contain 'token' field")
	}

	// Get token from DB (since it's only printed to console)
	var token string
	err := db.QueryRow("SELECT token FROM auth_tokens JOIN users ON users.uuid = auth_tokens.user_uuid WHERE users.email = ?", email).Scan(&token)
	if err != nil {
		t.Fatalf("failed to get token from DB: %v", err)
	}

	// 2. Login with token
	form = url.Values{}
	form.Add("email", email)
	form.Add("token", token)
	req = webReq(http.MethodPost, "/web/login", form, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Header().Get("HX-Redirect") != "/web/dashboard" {
		t.Errorf("expected HX-Redirect to /web/dashboard, got %q", rec.Header().Get("HX-Redirect"))
	}

	foundSession := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "session" {
			foundSession = true
			break
		}
	}
	if !foundSession {
		t.Error("expected session cookie in response")
	}
}

func TestWeb_Dashboard(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	email := "dash@test.com"
	createParent(t, r, email)

	// Get a token and session
	var token string
	db.QueryRow("INSERT INTO auth_tokens (token, user_uuid, expires_at) SELECT 'WWEBTOKN', uuid, datetime('now', '+10 minutes') FROM users WHERE email = ? RETURNING token", email).Scan(&token)

	cookie := getSessionCookie(t, r, email, "WWEBTOKN")

	// Access dashboard
	req := webReq(http.MethodGet, "/web/dashboard", nil, cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Timox Parent") {
		t.Error("expected dashboard content")
	}
}

func TestWeb_CreateChild(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	email := "parent_child@test.com"
	createParent(t, r, email)
	db.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) SELECT 'CTOKEN12', uuid, datetime('now', '+10 minutes') FROM users WHERE email = ?", email)
	cookie := getSessionCookie(t, r, email, "CTOKEN12")

	// Create child
	form := url.Values{}
	form.Add("name", "Web Kid")
	req := webReq(http.MethodPost, "/web/children", form, cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if !strings.Contains(rec.Header().Get("HX-Redirect"), "/web/children/") || !strings.Contains(rec.Header().Get("HX-Redirect"), "/welcome") {
		t.Errorf("expected redirect to welcome page, got %q", rec.Header().Get("HX-Redirect"))
	}

	// Verify child exists
	var count int
	db.QueryRow("SELECT count(*) FROM users WHERE name = 'Web Kid'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 child in DB, got %d", count)
	}
}

func TestWeb_ChildApps(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	email := "apps_test@test.com"
	p := createParent(t, r, email)
	db.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) SELECT 'APPSTOKN', uuid, datetime('now', '+10 minutes') FROM users WHERE email = ?", email)
	cookie := getSessionCookie(t, r, email, "APPSTOKN")

	// Create child and some usage
	childUUID := "child-123"
	db.Exec("INSERT INTO users (uuid, name, role) VALUES (?, 'Kiddo', 'child')", childUUID)
	db.Exec("INSERT INTO parent_children (parent_uuid, child_uuid) VALUES (?, ?)", p.UUID, childUUID)
	today := time.Now().Format("2006-01-02")
	db.Exec("INSERT INTO app_usage (user_uuid, package_name, usage_date, total_used_minutes, app_name) VALUES (?, 'com.test.app', ?, 45, 'Test App')", childUUID, today)

	// Get apps list (HTMX partial)
	req := webReq(http.MethodGet, "/web/children/"+childUUID+"/apps", nil, cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Test App") || !strings.Contains(rec.Body.String(), "45") {
		t.Error("expected app name and usage in response")
	}
}

func TestWeb_AuthMiddleware_Redirects(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	// Access authenticated page without session
	req := webReq(http.MethodGet, "/web/dashboard", nil, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/web/login" {
		t.Errorf("expected redirect to /web/login, got %d and Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestWeb_DeleteChild(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	email := "delete_web@test.com"
	p := createParent(t, r, email)
	db.Exec("INSERT INTO auth_tokens (token, user_uuid, expires_at) SELECT 'DELTOKEN', uuid, datetime('now', '+10 minutes') FROM users WHERE email = ?", email)
	cookie := getSessionCookie(t, r, email, "DELTOKEN")

	childUUID := "child-to-delete"
	db.Exec("INSERT INTO users (uuid, name, role) VALUES (?, 'DeleteMe', 'child')", childUUID)
	db.Exec("INSERT INTO parent_children (parent_uuid, child_uuid) VALUES (?, ?)", p.UUID, childUUID)

	// Delete
	req := webReq(http.MethodDelete, "/web/children/"+childUUID, nil, cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (HTMX partial), got %d", rec.Code)
	}

	// Verify gone from DB
	var count int
	db.QueryRow("SELECT count(*) FROM users WHERE uuid = ?", childUUID).Scan(&count)
	if count != 0 {
		t.Error("expected child to be deleted from DB")
	}
}
