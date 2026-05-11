package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	var err error
	db, err = setupDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
}

// createParent creates a parent user via POST /users and returns the User.
func createParent(t *testing.T, handler http.Handler, email string) User {
	t.Helper()
	body := `{"email":"` + email + `"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create parent: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var user User
	json.Unmarshal(rec.Body.Bytes(), &user)
	return user
}

// loginParent generates a token and logs in a parent, returning the JWT string.
func loginParent(t *testing.T, handler http.Handler, email string) string {
	t.Helper()

	// Generate auth token
	body := `{"email":"` + email + `"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth/token: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var tokenResp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &tokenResp)

	// Login
	loginBody := `{"email":"` + email + `","token":"` + tokenResp["token"] + `"}`
	req = httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(loginBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth/login: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var loginResp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &loginResp)
	return loginResp["jwt"]
}

// createChildViaParent creates a child via POST /children and returns the response map (uuid, name, role, token, expiresAt).
func createChildViaParent(t *testing.T, handler http.Handler, parentJWT, childName string) map[string]interface{} {
	t.Helper()
	req := authReq(http.MethodPost, "/children", `{"name":"`+childName+`"}`, parentJWT)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create child: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp
}

// loginChild uses child-login endpoint with a token, returns JWT.
func loginChild(t *testing.T, handler http.Handler, token string) string {
	t.Helper()
	body := `{"token":"` + token + `"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/child-login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("child-login: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp["jwt"]
}

func authReq(method, path string, body string, jwt string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	return req
}

// ===================== User creation tests =====================

func TestCreateUser_Parent(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	user := createParent(t, r, "parent@test.com")
	if user.UUID == "" {
		t.Error("expected non-empty uuid")
	}
	if user.Role != "parent" {
		t.Errorf("expected role parent, got %s", user.Role)
	}
}

func TestCreateUser_DuplicateEmail(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "dup@test.com")

	body := `{"email":"dup@test.com"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}

func TestCreateUser_MissingEmail(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ===================== Auth flow tests (parent) =====================

func TestAuthToken_Success(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "auth@test.com")

	body := `{"email":"auth@test.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp["token"]) != 8 {
		t.Errorf("expected 8-char token, got %q", resp["token"])
	}
	if resp["expiresAt"] == "" {
		t.Error("expected expiresAt")
	}
}

func TestAuthToken_UserNotFound(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	body := `{"email":"nobody@test.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAuthLogin_Success(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "login@test.com")
	jwt := loginParent(t, r, "login@test.com")
	if jwt == "" {
		t.Error("expected non-empty JWT")
	}
}

func TestAuthLogin_InvalidToken(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "bad@test.com")

	body := `{"email":"bad@test.com","token":"ZZZZZZZZ"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthLogin_TokenOneTimeUse(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "onetime@test.com")

	// Generate token
	body := `{"email":"onetime@test.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var tokenResp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &tokenResp)

	// First login should succeed
	loginBody := `{"email":"onetime@test.com","token":"` + tokenResp["token"] + `"}`
	req = httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(loginBody))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first login: expected 200, got %d", rec.Code)
	}

	// Second login with same token should fail
	req = httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(loginBody))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("second login: expected 401, got %d", rec.Code)
	}
}

// ===================== Child login tests =====================

func TestChildLogin_Success(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	token := childResp["token"].(string)

	jwt := loginChild(t, r, token)
	if jwt == "" {
		t.Error("expected non-empty JWT")
	}
}

func TestChildLogin_InvalidToken(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	body := `{"token":"ZZZZZZZZ"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/child-login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestChildLogin_TokenOneTimeUse(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	token := childResp["token"].(string)

	// First login succeeds
	loginChild(t, r, token)

	// Second login with same token should fail
	body := `{"token":"` + token + `"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/child-login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("second child-login: expected 401, got %d", rec.Code)
	}
}

// ===================== Parent generates child token =====================

func TestParentGenerateChildToken(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	childUUID := childResp["uuid"].(string)

	// Use the initial token to log in
	loginChild(t, r, childResp["token"].(string))

	// Parent generates a new token
	req := authReq(http.MethodPost, "/children/"+childUUID+"/token", "", parentJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var tokenResp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &tokenResp)
	if len(tokenResp["token"]) != 8 {
		t.Errorf("expected 8-char token, got %q", tokenResp["token"])
	}

	// Child can log in with the new token
	jwt := loginChild(t, r, tokenResp["token"])
	if jwt == "" {
		t.Error("expected non-empty JWT from new token")
	}
}

func TestParentGenerateChildToken_NotOwned(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent1@test.com")
	parent1JWT := loginParent(t, r, "parent1@test.com")

	createParent(t, r, "parent2@test.com")
	parent2JWT := loginParent(t, r, "parent2@test.com")

	// Parent1 creates a child
	childResp := createChildViaParent(t, r, parent1JWT, "kiddo")
	childUUID := childResp["uuid"].(string)

	// Parent2 tries to generate token for parent1's child
	req := authReq(http.MethodPost, "/children/"+childUUID+"/token", "", parent2JWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// ===================== Middleware tests =====================

func TestMiddleware_NoAuth(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	req := httptest.NewRequest(http.MethodGet, "/app_limits", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_InvalidJWT(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	req := httptest.NewRequest(http.MethodGet, "/app_limits", nil)
	req.Header.Set("Authorization", "Bearer invalid-jwt-string")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_WrongRole(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	// Parent trying to access child endpoint
	createParent(t, r, "parent@test.com")
	jwt := loginParent(t, r, "parent@test.com")

	req := authReq(http.MethodGet, "/app_limits", "", jwt)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// ===================== Child app_limits tests =====================

func TestChildAppLimits_GetOwn(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	childJWT := loginChild(t, r, childResp["token"].(string))

	req := authReq(http.MethodGet, "/app_limits", "", childJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Limits map[string]AppLimit `json:"limits"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Limits) != 0 {
		t.Errorf("expected 0 limits, got %d", len(resp.Limits))
	}
}

func TestChildAppLimits_ReadOnly(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	childJWT := loginChild(t, r, childResp["token"].(string))

	req := authReq(http.MethodPost, "/app_limits", `{"dailyLimitMinutes":10}`, childJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// ===================== Parent children tests =====================

func TestParentListChildren_Empty(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	jwt := loginParent(t, r, "parent@test.com")

	req := authReq(http.MethodGet, "/children", "", jwt)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var list []User
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("expected 0 children, got %d", len(list))
	}
}

func TestParentCreateChild(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	jwt := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, jwt, "My Kid")

	if childResp["role"] != "child" {
		t.Errorf("expected role child, got %s", childResp["role"])
	}
	if childResp["name"] != "My Kid" {
		t.Errorf("expected name 'My Kid', got %s", childResp["name"])
	}
	if childResp["token"] == nil || childResp["token"].(string) == "" {
		t.Error("expected a login token in response")
	}
	if childResp["expiresAt"] == nil || childResp["expiresAt"].(string) == "" {
		t.Error("expected expiresAt in response")
	}

	// Verify child appears in list
	req := authReq(http.MethodGet, "/children", "", jwt)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var list []User
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("expected 1 child, got %d", len(list))
	}
	if list[0].Name != "My Kid" {
		t.Errorf("expected child name 'My Kid', got %s", list[0].Name)
	}
}

func TestParentDeleteChild(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	jwt := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, jwt, "To Delete")
	childUUID := childResp["uuid"].(string)

	// Delete child
	req := authReq(http.MethodDelete, "/children/"+childUUID, "", jwt)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify child gone from list
	req = authReq(http.MethodGet, "/children", "", jwt)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var list []User
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("expected 0 children after delete, got %d", len(list))
	}
}

func TestParentDeleteChild_NotOwned(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent1@test.com")
	parent1JWT := loginParent(t, r, "parent1@test.com")

	createParent(t, r, "parent2@test.com")
	parent2JWT := loginParent(t, r, "parent2@test.com")

	// Parent1 creates a child
	childResp := createChildViaParent(t, r, parent1JWT, "kiddo")
	childUUID := childResp["uuid"].(string)

	// Parent2 tries to delete parent1's child
	req := authReq(http.MethodDelete, "/children/"+childUUID, "", parent2JWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// ===================== Parent managing child limits =====================

func TestParentGetChildLimits(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	jwt := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, jwt, "kiddo")
	childUUID := childResp["uuid"].(string)

	// Get child limits
	req := authReq(http.MethodGet, "/children/"+childUUID+"/app_limits", "", jwt)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var limits map[string]AppLimit
	json.Unmarshal(rec.Body.Bytes(), &limits)
	if len(limits) != 0 {
		t.Errorf("expected 0 limits, got %d", len(limits))
	}
}

func TestParentUpdateChildLimit(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	jwt := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, jwt, "kiddo")
	childUUID := childResp["uuid"].(string)

	// Create then update a limit
	reqCreate := authReq(http.MethodPost, "/children/"+childUUID+"/app_limits/com.test.app", `{"dailyLimitMinutes":30}`, jwt)
	recCreate := httptest.NewRecorder()
	r.ServeHTTP(recCreate, reqCreate)

	req := authReq(http.MethodPost, "/children/"+childUUID+"/app_limits/com.test.app", `{"dailyLimitMinutes":99}`, jwt)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var limit AppLimit
	json.Unmarshal(rec.Body.Bytes(), &limit)
	if limit.DailyLimitMinutes != 99 {
		t.Errorf("expected 99, got %d", limit.DailyLimitMinutes)
	}
}

func TestParentCreateNewChildLimit(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	jwt := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, jwt, "kiddo")
	childUUID := childResp["uuid"].(string)

	// Add new limit
	req := authReq(http.MethodPost, "/children/"+childUUID+"/app_limits/com.new.app", `{"dailyLimitMinutes":45}`, jwt)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Verify count
	req = authReq(http.MethodGet, "/children/"+childUUID+"/app_limits", "", jwt)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var limits map[string]AppLimit
	json.Unmarshal(rec.Body.Bytes(), &limits)
	if len(limits) != 1 {
		t.Errorf("expected 1 limit, got %d", len(limits))
	}
}

func TestParentDeleteChildLimit(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	jwt := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, jwt, "kiddo")
	childUUID := childResp["uuid"].(string)

	// Create a limit then delete it
	reqCreate := authReq(http.MethodPost, "/children/"+childUUID+"/app_limits/com.test.app", `{"dailyLimitMinutes":30}`, jwt)
	recCreate := httptest.NewRecorder()
	r.ServeHTTP(recCreate, reqCreate)

	req := authReq(http.MethodDelete, "/children/"+childUUID+"/app_limits/com.test.app", "", jwt)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	// Verify gone
	req = authReq(http.MethodGet, "/children/"+childUUID+"/app_limits", "", jwt)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var limits map[string]AppLimit
	json.Unmarshal(rec.Body.Bytes(), &limits)
	if len(limits) != 0 {
		t.Errorf("expected 0 limits after delete, got %d", len(limits))
	}
}

func TestParentChildLimits_NotOwned(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent1@test.com")
	parent1JWT := loginParent(t, r, "parent1@test.com")

	createParent(t, r, "parent2@test.com")
	parent2JWT := loginParent(t, r, "parent2@test.com")

	childResp := createChildViaParent(t, r, parent1JWT, "kiddo")
	childUUID := childResp["uuid"].(string)

	req := authReq(http.MethodGet, "/children/"+childUUID+"/app_limits", "", parent2JWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// ===================== Integration: full auth + limits flow =====================

func TestFullAuthFlow(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	// 1. Create parent
	createParent(t, r, "parent@test.com")

	// 2. Login parent
	parentJWT := loginParent(t, r, "parent@test.com")

	// 3. Parent creates a child
	childResp := createChildViaParent(t, r, parentJWT, "Managed Kid")
	childUUID := childResp["uuid"].(string)
	childToken := childResp["token"].(string)

	// 4. Child logs in with token
	childJWT := loginChild(t, r, childToken)

	// 5. Child can read own limits
	req := authReq(http.MethodGet, "/app_limits", "", childJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("child GET /app_limits: expected 200, got %d", rec.Code)
	}

	// 6. Child cannot access parent endpoints
	req = authReq(http.MethodGet, "/children", "", childJWT)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("child GET /children: expected 403, got %d", rec.Code)
	}

	// 7. Parent can manage the child's limits
	req = authReq(http.MethodPost, "/children/"+childUUID+"/app_limits/com.test.app", `{"dailyLimitMinutes":15}`, parentJWT)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("parent POST limit: expected 200, got %d", rec.Code)
	}

	// 8. Parent generates a new token for child (since old one was consumed)
	req = authReq(http.MethodPost, "/children/"+childUUID+"/token", "", parentJWT)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("generate token: expected 200, got %d", rec.Code)
	}
	var newTokenResp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &newTokenResp)

	// 9. Child logs in with new token and sees updated limits
	childJWT = loginChild(t, r, newTokenResp["token"])
	req = authReq(http.MethodGet, "/app_limits", "", childJWT)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("managed child GET /app_limits: expected 200, got %d", rec.Code)
	}
	var resp struct {
		Limits map[string]AppLimit `json:"limits"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Limits) != 1 {
		t.Errorf("expected 1 limit (parent-set), got %d", len(resp.Limits))
	}
	if resp.Limits["com.test.app"].DailyLimitMinutes != 15 {
		t.Errorf("expected 15 minutes for com.test.app, got %d", resp.Limits["com.test.app"].DailyLimitMinutes)
	}

	// 10. Parent deletes the child
	req = authReq(http.MethodDelete, "/children/"+childUUID, "", parentJWT)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("parent DELETE child: expected 204, got %d", rec.Code)
	}

	// 11. Parent cannot access deleted child
	req = authReq(http.MethodGet, "/children/"+childUUID+"/app_limits", "", parentJWT)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for deleted child, got %d", rec.Code)
	}
}

// ===================== App usage report tests =====================

func TestReportAppUsage_Success(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")

	childResp := createChildViaParent(t, r, parentJWT, "reporter")
	childUUID := childResp["uuid"].(string)
	childJWT := loginChild(t, r, childResp["token"].(string))

	body := `[{"packageName":"com.example.game","totalUsedMinutes":100},{"packageName":"com.example.chat","totalUsedMinutes":30}]`
	req := authReq(http.MethodPost, "/report", body, childJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the data was inserted
	var count int
	var totalUsed int
	err := db.QueryRow("SELECT count(*), total_used_minutes FROM app_usage WHERE user_uuid = ? AND package_name = ?", childUUID, "com.example.game").Scan(&count, &totalUsed)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 usage entry, got %d", count)
	}
	if totalUsed != 100 {
		t.Errorf("expected totalUsedMinutes 100, got %d", totalUsed)
	}
}

func TestReportAppUsage_InvalidBody(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "reporter")
	childJWT := loginChild(t, r, childResp["token"].(string))

	body := `{"totalUsedMinutes": "not-a-number"}` // Not an array
	req := authReq(http.MethodPost, "/report", body, childJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReportAppUsage_NegativeUsedMinutes(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "reporter")
	childJWT := loginChild(t, r, childResp["token"].(string))

	body := `[{"packageName":"com.example.negative","totalUsedMinutes":-10}]`
	req := authReq(http.MethodPost, "/report", body, childJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReportAppUsage_Unauthorized(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	body := `[{"packageName":"com.example.unauth","totalUsedMinutes":50}]`
	req := httptest.NewRequest(http.MethodPost, "/report", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestReportAppUsage_UpdateExisting(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "updater")
	childUUID := childResp["uuid"].(string)
	childJWT := loginChild(t, r, childResp["token"].(string))

	// First report
	body1 := `[{"packageName":"com.example.update","totalUsedMinutes":50}]`
	req1 := authReq(http.MethodPost, "/report", body1, childJWT)
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("first report: expected 204, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Second report (update)
	body2 := `[{"packageName":"com.example.update","totalUsedMinutes":150}]`
	req2 := authReq(http.MethodPost, "/report", body2, childJWT)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("second report: expected 204, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Verify the data was updated
	var count int
	var totalUsed int
	err := db.QueryRow("SELECT count(*), total_used_minutes FROM app_usage WHERE user_uuid = ? AND package_name = ?", childUUID, "com.example.update").Scan(&count, &totalUsed)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 usage entry after update, got %d", count)
	}
	if totalUsed != 150 {
		t.Errorf("expected totalUsedMinutes 150 after update, got %d", totalUsed)
	}
}

func TestReportAppUsage_LastReportedWins(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "lastreported")
	childUUID := childResp["uuid"].(string)
	childJWT := loginChild(t, r, childResp["token"].(string))

	// Simulate stale high value (e.g., carried over from previous day at UTC midnight)
	body1 := `[{"packageName":"com.example.reset","totalUsedMinutes":900}]`
	req1 := authReq(http.MethodPost, "/report", body1, childJWT)
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("first report: expected 204, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Simulate phone reset at local midnight: reports 0, then small values
	body2 := `[{"packageName":"com.example.reset","totalUsedMinutes":5}]`
	req2 := authReq(http.MethodPost, "/report", body2, childJWT)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("second report: expected 204, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var totalUsed int
	db.QueryRow("SELECT total_used_minutes FROM app_usage WHERE user_uuid = ? AND package_name = ?", childUUID, "com.example.reset").Scan(&totalUsed)
	if totalUsed != 5 {
		t.Errorf("expected last-reported value 5, got %d (MAX would have kept 900)", totalUsed)
	}
}

func TestReportAppUsage_ParentForbidden(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	jwt := loginParent(t, r, "parent@test.com")

	body := `[{"packageName":"com.example.game","totalUsedMinutes":100}]`
	req := authReq(http.MethodPost, "/report", body, jwt)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReportAppUsage_EmptyArray(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "empty")
	childJWT := loginChild(t, r, childResp["token"].(string))

	body := `[]`
	req := authReq(http.MethodPost, "/report", body, childJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReportAppUsage_MissingAppName(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "noname")
	childJWT := loginChild(t, r, childResp["token"].(string))

	body := `[{"totalUsedMinutes":50}]`
	req := authReq(http.MethodPost, "/report", body, childJWT)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ===================== Snapshot deduplication tests =====================

func TestUpdateSnapshots(t *testing.T) {
	cases := []struct {
		name      string
		existing  [][]int
		mod       int
		newMin    int
		wantLen   int
		wantLast  []int
	}{
		{"first insert", nil, 60, 10, 1, []int{60, 10}},
		{"option B same value", [][]int{{60, 10}}, 61, 10, 1, []int{60, 10}},
		{"option B lower value", [][]int{{60, 10}}, 61, 5, 1, []int{60, 10}},
		{"option A same minute replace", [][]int{{60, 10}}, 60, 15, 1, []int{60, 15}},
		{"option A same minute with prior", [][]int{{59, 8}, {60, 10}}, 60, 15, 2, []int{60, 15}},
		{"append different minute", [][]int{{60, 10}}, 61, 15, 2, []int{61, 15}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := updateSnapshots(tc.existing, tc.mod, tc.newMin)
			if len(got) != tc.wantLen {
				t.Fatalf("len=%d want %d (got %v)", len(got), tc.wantLen, got)
			}
			last := got[len(got)-1]
			if last[0] != tc.wantLast[0] || last[1] != tc.wantLast[1] {
				t.Errorf("last=%v want %v", last, tc.wantLast)
			}
		})
	}
}

func TestReportAppUsage_DuplicateReportSkipsSnapshot(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "dupchild")
	childUUID := childResp["uuid"].(string)
	childJWT := loginChild(t, r, childResp["token"].(string))

	body := `{"timestamp":"2026-01-15T10:00:00+00:00","apps":[{"packageName":"com.example.app","totalUsedMinutes":30}]}`
	send := func() {
		req := authReq(http.MethodPost, "/report", body, childJWT)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	send()
	send() // exact duplicate

	var snapshotsJSON string
	db.QueryRow("SELECT snapshots FROM app_usage WHERE user_uuid = ? AND package_name = ?", childUUID, "com.example.app").Scan(&snapshotsJSON)

	var snapshots [][]float64
	if err := json.Unmarshal([]byte(snapshotsJSON), &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Errorf("expected 1 snapshot after duplicate report, got %d: %s", len(snapshots), snapshotsJSON)
	}
}

func TestReportAppUsage_SameMinuteHigherValueReplacesSnapshot(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "optionachild")
	childUUID := childResp["uuid"].(string)
	childJWT := loginChild(t, r, childResp["token"].(string))

	send := func(minutes int) {
		body := fmt.Sprintf(`{"timestamp":"2026-01-15T10:00:00+00:00","apps":[{"packageName":"com.example.app","totalUsedMinutes":%d}]}`, minutes)
		req := authReq(http.MethodPost, "/report", body, childJWT)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	send(10) // first report at minute 600
	send(15) // same timestamp (minute 600), higher value — Option A: replace

	var snapshotsJSON string
	db.QueryRow("SELECT snapshots FROM app_usage WHERE user_uuid = ? AND package_name = ?", childUUID, "com.example.app").Scan(&snapshotsJSON)

	var snapshots [][]float64
	if err := json.Unmarshal([]byte(snapshotsJSON), &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Errorf("Option A: expected 1 snapshot (replace), got %d: %s", len(snapshots), snapshotsJSON)
	}
	if len(snapshots) == 1 && snapshots[0][1] != 15 {
		t.Errorf("Option A: expected snapshot value 15, got %v", snapshots[0][1])
	}
}

func TestReportAppUsage_IncrementalMinutesAppend(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "incchild")
	childUUID := childResp["uuid"].(string)
	childJWT := loginChild(t, r, childResp["token"].(string))

	reports := []string{
		`{"timestamp":"2026-01-15T10:00:00+00:00","apps":[{"packageName":"com.example.app","totalUsedMinutes":1}]}`,
		`{"timestamp":"2026-01-15T10:01:00+00:00","apps":[{"packageName":"com.example.app","totalUsedMinutes":2}]}`,
		`{"timestamp":"2026-01-15T10:02:00+00:00","apps":[{"packageName":"com.example.app","totalUsedMinutes":3}]}`,
	}
	for _, body := range reports {
		req := authReq(http.MethodPost, "/report", body, childJWT)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	var snapshotsJSON string
	db.QueryRow("SELECT snapshots FROM app_usage WHERE user_uuid = ? AND package_name = ?", childUUID, "com.example.app").Scan(&snapshotsJSON)

	var snapshots [][]float64
	if err := json.Unmarshal([]byte(snapshotsJSON), &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 3 {
		t.Errorf("expected 3 snapshots for 3 distinct minutes, got %d: %s", len(snapshots), snapshotsJSON)
	}
}

// ===================== JWT renewal tests =====================

// generateExpiredJWT mints a correctly-signed JWT whose exp is one hour in the past.
func generateExpiredJWT(t *testing.T, userUUID, role string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":  userUUID,
		"role": role,
		"exp":  time.Now().Add(-time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("generateExpiredJWT: %v", err)
	}
	return s
}

// renewJWT calls POST /auth/renew and returns the HTTP status and the new JWT string (empty on non-200).
func renewJWT(t *testing.T, handler http.Handler, bearerJWT string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/renew", nil)
	req.Header.Set("Authorization", "Bearer "+bearerJWT)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, ""
	}
	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp["jwt"]
}

func TestRenewJWT_ChildSuccess(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	childJWT := loginChild(t, r, childResp["token"].(string))

	code, newJWT := renewJWT(t, r, childJWT)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if newJWT == "" {
		t.Fatal("expected non-empty jwt in response")
	}
}

func TestRenewJWT_ParentSuccess(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")

	code, newJWT := renewJWT(t, r, parentJWT)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if newJWT == "" {
		t.Fatal("expected non-empty jwt in response")
	}
}

func TestRenewJWT_NoAuth(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	req := httptest.NewRequest(http.MethodPost, "/auth/renew", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRenewJWT_InvalidJWT(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	code, _ := renewJWT(t, r, "not.a.valid.jwt")
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
}

func TestRenewJWT_ExpiredJWT(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	childUUID := childResp["uuid"].(string)

	expiredJWT := generateExpiredJWT(t, childUUID, "child")
	code, _ := renewJWT(t, r, expiredJWT)
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired JWT, got %d", code)
	}
}

func TestRenewJWT_NewTokenPreservesChildRole(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	childJWT := loginChild(t, r, childResp["token"].(string))

	_, newJWT := renewJWT(t, r, childJWT)
	if newJWT == "" {
		t.Fatal("expected non-empty jwt after renewal")
	}

	// New JWT must still allow child endpoints
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, authReq(http.MethodGet, "/app_limits", "", newJWT))
	if rec.Code != http.StatusOK {
		t.Errorf("renewed child JWT: GET /app_limits expected 200, got %d", rec.Code)
	}

	// New JWT must still deny parent-only endpoints
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, authReq(http.MethodGet, "/children", "", newJWT))
	if rec.Code != http.StatusForbidden {
		t.Errorf("renewed child JWT: GET /children expected 403, got %d", rec.Code)
	}
}

func TestRenewJWT_NewTokenPreservesParentRole(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")

	_, newJWT := renewJWT(t, r, parentJWT)
	if newJWT == "" {
		t.Fatal("expected non-empty jwt after renewal")
	}

	// New JWT must still allow parent endpoints
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, authReq(http.MethodGet, "/children", "", newJWT))
	if rec.Code != http.StatusOK {
		t.Errorf("renewed parent JWT: GET /children expected 200, got %d", rec.Code)
	}

	// New JWT must still deny child-only endpoints
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, authReq(http.MethodGet, "/app_limits", "", newJWT))
	if rec.Code != http.StatusForbidden {
		t.Errorf("renewed parent JWT: GET /app_limits expected 403, got %d", rec.Code)
	}
}

func TestRenewJWT_DeletedUser(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	childJWT := loginChild(t, r, childResp["token"].(string))
	childUUID := childResp["uuid"].(string)

	db.Exec("DELETE FROM users WHERE uuid = ?", childUUID)

	code, _ := renewJWT(t, r, childJWT)
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for deleted user JWT, got %d", code)
	}
}

func TestRenewJWT_OriginalJWTStillWorksAfterRenewal(t *testing.T) {
	setupTestDB(t)
	r := newRouter()

	// The server has no revocation list — the original JWT must remain valid
	// until its own expiry even after a new one has been issued.
	createParent(t, r, "parent@test.com")
	parentJWT := loginParent(t, r, "parent@test.com")
	childResp := createChildViaParent(t, r, parentJWT, "kiddo")
	childJWT := loginChild(t, r, childResp["token"].(string))

	_, newJWT := renewJWT(t, r, childJWT)
	if newJWT == "" {
		t.Fatal("expected non-empty jwt after renewal")
	}

	// Original JWT must still be accepted
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, authReq(http.MethodGet, "/app_limits", "", childJWT))
	if rec.Code != http.StatusOK {
		t.Errorf("original JWT after renewal: GET /app_limits expected 200, got %d", rec.Code)
	}
}
