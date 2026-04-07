# timox_server API Reference

Base URL: `http://localhost:8080`  
Auth: JWT via `Authorization: Bearer <token>` header  
All request/response bodies are JSON unless noted.

---

## Public Endpoints

### POST /users
Create a new parent account.

**Request:**
```json
{ "email": "parent@example.com" }
```
**Response 201:**
```json
{ "uuid": "...", "email": "parent@example.com", "role": "parent" }
```
**Errors:** 400 (missing email), 409 (email already exists)

---

### POST /auth/token
Request a one-time login token sent to the parent's email.

**Request:**
```json
{ "email": "parent@example.com" }
```
**Response 200:**
```json
{ "token": "ABCDEFGH", "expiresAt": "2024-01-15T10:40:00Z" }
```
Tokens are 8 uppercase letters, expire in 10 minutes, one-time use.  
**Errors:** 400, 404 (user not found)

---

### POST /auth/login
Exchange an email + one-time token for a parent JWT.

**Request:**
```json
{ "email": "parent@example.com", "token": "ABCDEFGH" }
```
**Response 200:**
```json
{ "jwt": "<jwt>" }
```
**Errors:** 400, 401 (invalid credentials or expired token)

---

### POST /auth/child-login
Exchange a one-time token for a child JWT (no email needed).

**Request:**
```json
{ "token": "ABCDEFGH" }
```
**Response 200:**
```json
{ "jwt": "<jwt>", "name": "Alice" }
```
**Errors:** 400, 401 (invalid or expired token)

---

## Authenticated Endpoints

### GET /me
Returns the authenticated user's profile. Works for both parents and children.

**Response 200:**
```json
{ "uuid": "...", "name": "Alice", "role": "child" }
```
`name` is empty for parents; `email` is not returned here.

---

## Child Endpoints
Require `Authorization: Bearer <jwt>` with `role=child`.

### GET /app_limits
Returns the child's own effective limits plus global schedule. Also promotes any pending limits whose 24h delay has elapsed.

**Response 200:**
```json
{
  "globalSchedule": [
    { "blockingStartTime": "22:00", "blockingEndTime": "07:00" }
  ],
  "limits": {
    "com.example.app": {
      "dailyLimitMinutes": 60,
      "blockType": 0,
      "schedule": [
        { "blockingStartTime": "15:00", "blockingEndTime": "18:00" }
      ]
    },
    "GLOBAL_TIME": {
      "dailyLimitMinutes": 120,
      "blockType": 0
    }
  }
}
```
`globalSchedule` is an empty array `[]` if no global schedule is set.  
`schedule` inside a limit is omitted when the app has no per-app schedule.  
`GLOBAL_TIME` is a special package name for the total daily screen-time limit.

**blockType values:**
- `0` (Normal): daily limit + global schedule apply
- `1` (Blocked): always blocked, no exceptions
- `2` (GlobalExempt): exempt from global schedule; per-app limit still applies
- `3` (Unrestricted): never blocked, all limits ignored

---

### GET /limits_version
Lightweight poll endpoint. Returns the timestamp of the last effective limit change so the child app can detect when to re-fetch `/app_limits`.

**Response 200:**
```json
{ "updatedAt": "2024-01-15T10:30:00Z" }
```
`updatedAt` is `null` if limits have never been explicitly changed:
```json
{ "updatedAt": null }
```

**When `updatedAt` is set:**
- Parent changes or deletes an app limit (immediate apply)
- Parent changes block type
- Parent adds/removes an app schedule
- Parent changes or clears the global schedule
- A delayed (24h) limit increase auto-promotes

**Not updated for:** creation of a pending increase (limits are unchanged until it applies).

---

### POST /report
Report today's app usage. Preserves the higher value on conflict (i.e. usage only increases within a day).

**Request:** array of usage entries
```json
[
  { "packageName": "com.example.app", "appName": "Example App", "totalUsedMinutes": 45 },
  { "packageName": "com.other.app", "appName": "", "totalUsedMinutes": 10 }
]
```
`appName` is optional (empty string is treated as null).  
**Response 204** (no body)  
**Errors:** 400 (empty array, missing packageName, negative minutes)

---

### POST /icons/check
Ask the server which packages it still needs icons for. Only packages the child has previously reported via `/report` are considered; any other package names in the request are silently ignored.

The server returns packages that have no approved icon yet **and** for which this child has not already submitted a pending icon.

**Request:**
```json
["com.example.app", "com.other.app"]
```
**Response 200:**
```json
["com.example.app"]
```
Returns an empty array `[]` if no icons are needed.

---

### POST /icons
Upload an app icon. The child device should call `/icons/check` first and only upload icons the server requested. Icons are queued for admin review before becoming publicly visible.

Max request body: **512 KB**. Resize icons to **96×96** before encoding to stay well within the limit.

**Request:**
```json
{
  "packageName": "com.example.app",
  "appName": "Example App",
  "iconBase64": "<base64-encoded PNG>"
}
```
**Response 204** (no body)  
**Errors:** 400 (missing fields, invalid base64), 413 (body too large)

---

### GET /apps/{packages}
Look up metadata for one or more apps by package name. `{packages}` is a comma-separated list.

Only returns entries for apps that have been approved by an admin (i.e. exist in the `apps` table with a non-null `icon_path`).

**Example:** `GET /apps/com.example.app,com.other.app`

**Response 200:**
```json
[
  {
    "packageName": "com.example.app",
    "appName": "Example App",
    "iconPath": "/static/icons/com.example.app.png"
  }
]
```
`iconPath` is an absolute path without domain — prepend the server's base URL to construct the full URL. Icons are served by Caddy (not by the Go server) at the path indicated.

Packages not found in the `apps` table are omitted from the response (not an error).

---

## Parent Endpoints
Require `Authorization: Bearer <jwt>` with `role=parent`. All child operations verify the parent owns the child.

### GET /children
List all children belonging to the authenticated parent.

**Response 200:**
```json
[
  { "uuid": "...", "name": "Alice", "role": "child" },
  { "uuid": "...", "name": "Bob", "role": "child" }
]
```
Returns `[]` (not null) when no children exist.

---

### POST /children
Create a new child and auto-generate a 10-minute login token.

**Request:**
```json
{ "name": "Alice" }
```
**Response 201:**
```json
{
  "uuid": "...",
  "name": "Alice",
  "role": "child",
  "token": "ABCDEFGH",
  "expiresAt": "2024-01-15T10:40:00Z"
}
```
**Errors:** 400 (missing name)

---

### DELETE /children/{childUUID}
Delete a child and all associated data (CASCADE).

**Response 204**  
**Errors:** 404 (child not found or not owned by parent)

---

### POST /children/{childUUID}/token
Generate a new one-time login token for a child (10-minute expiry). Multiple tokens can exist simultaneously.

**Response 200:**
```json
{ "token": "ABCDEFGH", "expiresAt": "2024-01-15T10:40:00Z" }
```
**Errors:** 404

---

### GET /children/{childUUID}/usage
Get a child's app usage for a specific date.

**Query params:** `?date=2024-01-15` (defaults to today, format `YYYY-MM-DD`)

**Response 200:**
```json
[
  { "packageName": "com.example.app", "totalUsedMinutes": 45 },
  { "packageName": "com.other.app", "totalUsedMinutes": 10 }
]
```
Returns `[]` when no usage recorded. Sorted by `totalUsedMinutes` descending.  
**Errors:** 404

---

### GET /children/{childUUID}/app_limits
Get all app limits for a child (parent view). Does not apply pending limits.

**Response 200:**
```json
{
  "com.example.app": {
    "dailyLimitMinutes": 60,
    "blockType": 0,
    "schedule": [{ "blockingStartTime": "15:00", "blockingEndTime": "18:00" }]
  },
  "GLOBAL_TIME": {
    "dailyLimitMinutes": 120,
    "blockType": 0
  }
}
```
`schedule` is omitted when the app has no per-app schedule.  
**Errors:** 404

---

### POST /children/{childUUID}/app_limits/{packageName}
Set or update a daily limit for a specific app (or `GLOBAL_TIME` for total screen time).

**Request:**
```json
{ "dailyLimitMinutes": 90 }
```

**Delayed changes rule:** If the child has `delayed_changes` enabled AND this is an increase to an existing `BlockTypeNormal` limit, the change is queued with a 24-hour delay.

**Response 200 (immediate apply):**
```json
{ "dailyLimitMinutes": 90, "blockType": 0 }
```

**Response 200 (queued as pending):**
```json
{
  "dailyLimitMinutes": 60,
  "blockType": 0,
  "pending": true,
  "pendingLimit": 90,
  "appliesAt": "2024-01-16 10:30:00"
}
```
Returns the current (unchanged) limit with pending info.  
**Errors:** 400 (invalid JSON), 404

---

### DELETE /children/{childUUID}/app_limits/{packageName}
Remove an app limit entirely (also clears any pending increase for that app).

**Response 204**  
**Errors:** 404 (limit not found or child not owned)

---

## Data Model Notes

- **GLOBAL_TIME**: special package name stored in `app_limits` representing the total daily screen-time cap across all apps.
- **Pending limits**: only created for increases on `BlockTypeNormal` limits when delayed changes is on. Decreases, new limits, and block-type changes are always immediate.
- **Schedules**: times are stored/returned as `HH:MM` strings (24-hour). A schedule blocks the app during `blockingStartTime`–`blockingEndTime`. Overnight ranges (e.g. 22:00–07:00) are supported.
- **JWT expiry**: 30 days from issue.
- **App icons**: stored as PNG files on disk (`$ICONS_DIR/icons/{packageName}.png`). Served statically by Caddy at `/static/icons/`. Pending (unreviewed) icons live at `$ICONS_DIR/pending-icons/{userUUID}-{packageName}.png` and are served at `/static/pending-icons/` (admin use only).
