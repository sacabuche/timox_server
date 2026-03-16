package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
