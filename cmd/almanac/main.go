// Command almanac is the HTTP backend server for the Almanac personal platform.
//
// MVP stage: a minimal HTTP service exposing a /health endpoint, used to
// validate the end-to-end CI/CD pipeline (build -> test -> deploy).
package main

import (
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mutouyun/almanac/internal/store"
)

// sessionCookie is the name of the cookie holding the session token.
const sessionCookie = "almanac_session"

// sessionTTL is how long a login session stays valid.
const sessionTTL = 7 * 24 * time.Hour

// healthResponse is the payload returned by the /health endpoint.
type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Time    string `json:"time"`
}

// cstZone is China Standard Time (UTC+8), used for human-facing timestamps.
var cstZone = time.FixedZone("CST", 8*60*60)

// dbCheckResponse is the payload returned by the /db-check endpoint.
type dbCheckResponse struct {
	Status string `json:"status"`
	Visits int64  `json:"visits"`
	Time   string `json:"time"`
}

// loginRequest is the payload for POST /api/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse is returned on successful login.
type loginResponse struct {
	Status   string `json:"status"`
	Username string `json:"username"`
}

// errorResponse is a generic error payload.
type errorResponse struct {
	Error string `json:"error"`
}

func loginHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request"})
			return
		}

		u, err := st.VerifyLogin(req.Username, req.Password)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid credentials"})
			return
		}

		token, err := st.CreateSession(u.ID, sessionTTL)
		if err != nil {
			log.Printf("failed to create session: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    token,
			Path:     "/",
			MaxAge:   int(sessionTTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})

		_ = json.NewEncoder(w).Encode(loginResponse{
			Status:   "ok",
			Username: u.Username,
		})
	}
}

func logoutHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err == nil {
			_ = st.DeleteSession(c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:   sessionCookie,
			Path:   "/",
			MaxAge: -1,
		})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// whoamiResponse is returned by GET /api/whoami.
type whoamiResponse struct {
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
}

func whoamiHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		u, err := st.UserBySession(c.Value)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(whoamiResponse{Username: u.Username, IsAdmin: u.IsAdmin})
	}
}

// changePasswordRequest is the payload for POST /api/change-password.
type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// minPasswordLen is the minimum accepted length for a new password.
const minPasswordLen = 6

func changePasswordHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		// Authenticate via session cookie.
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}
		u, err := st.UserBySession(c.Value)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}

		var req changePasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request"})
			return
		}
		if len(req.NewPassword) < minPasswordLen {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "password too short"})
			return
		}

		err = st.ChangePassword(u.ID, req.OldPassword, req.NewPassword)
		if err == store.ErrWrongPassword {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "wrong old password"})
			return
		}
		if err != nil {
			log.Printf("change password error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
			return
		}

		// Invalidate all sessions (force re-login) and clear the current cookie.
		if err := st.DeleteUserSessions(u.ID); err != nil {
			log.Printf("failed to clear sessions after password change: %v", err)
		}
		http.SetCookie(w, &http.Cookie{
			Name:   sessionCookie,
			Path:   "/",
			MaxAge: -1,
		})
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// currentUser resolves the authenticated user from the session cookie, or nil.
func currentUser(st *store.Store, r *http.Request) *store.User {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	u, err := st.UserBySession(c.Value)
	if err != nil {
		return nil
	}
	return u
}

// tokenResponse carries a webhook token plus the ingestion path, so the UI can
// show a copy-paste-ready endpoint.
type tokenResponse struct {
	Token string `json:"token"`
	Path  string `json:"path"`
}

func webhookTokenHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{Token: u.WebhookToken, Path: "/api/webhook/entry"})
	}
}

func webhookTokenResetHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}
		token, err := st.RegenerateWebhookToken(u.ID)
		if err != nil {
			log.Printf("reset webhook token error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
			return
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{Token: token, Path: "/api/webhook/entry"})
	}
}

// entriesResponse is the payload for GET /api/entries.
type entriesResponse struct {
	Entries []store.EntryRow `json:"entries"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

// manualEntryRequest is the body for POST /api/entries (create) and PUT
// /api/entries/{id} (edit). Amount is json.Number (never float64) so we reuse
// parseAmountToCents; the stored value is unsigned cents (absolute magnitude).
// Direction is NOT sent by the client: it is derived from the chosen category.
// CategoryID nil/omitted means "待分类" (unclassified).
type manualEntryRequest struct {
	Amount     json.Number `json:"amount"`      // yuan, unsigned magnitude
	RawType    string      `json:"raw_type"`    // short summary/type label
	RecordTime string      `json:"record_time"` // free-form time, normalized server-side
	Note       string      `json:"note"`
	CategoryID *int64      `json:"category_id"`
}

// parseManualEntry validates and normalizes a manual entry request into a
// store.ManualEntryInput. It maps amount/time errors to a client message.
func parseManualEntry(req manualEntryRequest) (store.ManualEntryInput, string) {
	cents, err := parseAmountToCents(req.Amount.String())
	if err == errZeroAmount {
		return store.ManualEntryInput{}, "amount must not be zero"
	}
	if err != nil {
		return store.ManualEntryInput{}, "invalid amount"
	}
	if cents < 0 {
		cents = -cents // store unsigned; ignore any legacy sign
	}
	recordTime, err := normalizeRecordTime(req.RecordTime)
	if err != nil {
		return store.ManualEntryInput{}, "invalid time"
	}
	if strings.TrimSpace(req.RawType) == "" {
		return store.ManualEntryInput{}, "summary is required"
	}
	return store.ManualEntryInput{
		CategoryID:  req.CategoryID,
		AmountCents: cents,
		RawType:     strings.TrimSpace(req.RawType),
		RecordTime:  recordTime,
		Note:        strings.TrimSpace(req.Note),
	}, ""
}

// entriesHandler serves GET (paginated list) and POST (create manual entry) on
// /api/entries. Requires a valid session cookie.
func entriesHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			entriesListHandler(st, w, r, u)
		case http.MethodPost:
			entriesCreateHandler(st, w, r, u)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// entriesCreateHandler handles POST /api/entries: create a manual entry.
func entriesCreateHandler(st *store.Store, w http.ResponseWriter, r *http.Request, u *store.User) {
	var req manualEntryRequest
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
		return
	}
	in, msg := parseManualEntry(req)
	if msg != "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
		return
	}
	id, err := st.CreateManualEntry(u.ID, in)
	if err != nil {
		switch err {
		case store.ErrCategoryNotFound:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "category not found"})
		case store.ErrInvalidAmount, store.ErrInvalidEntry:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: err.Error()})
		default:
			log.Printf("create manual entry error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
		}
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "id": id})
}

// entriesListHandler handles GET /api/entries: paginated list, newest first.
func entriesListHandler(st *store.Store, w http.ResponseWriter, r *http.Request, u *store.User) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}

	entries, total, err := st.ListEntries(u.ID, limit, offset)
	if err != nil {
		log.Printf("list entries error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
		return
	}
	_ = json.NewEncoder(w).Encode(entriesResponse{
		Entries: entries,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	})
}

// currentMonthKey returns the current Asia/Shanghai month as "YYYY-MM". It
// falls back to UTC-derived value if the timezone database is unavailable.
func currentMonthKey() string {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return time.Now().In(loc).Format("2006-01")
}

// summaryHandler serves GET /api/summary?month=YYYY-MM with the month's
// income/expense/balance plus unclassified totals and prior-month figures.
// Month defaults to the current Asia/Shanghai month when omitted.
func summaryHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}
		month := r.URL.Query().Get("month")
		if month == "" {
			month = currentMonthKey()
		}
		sum, err := st.SummaryByMonth(u.ID, month)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid month"})
			return
		}
		_ = json.NewEncoder(w).Encode(sum)
	}
}

// summaryByCategoryResponse wraps the per-category breakdown slices.
type summaryByCategoryResponse struct {
	Month     string                `json:"month"`
	Direction int                   `json:"direction"`
	Slices    []store.CategorySlice `json:"slices"`
}

// summaryByCategoryHandler serves GET /api/summary/by-category?month=YYYY-MM&direction=-1
// with per-category totals for one direction, feeding the breakdown chart.
// direction defaults to -1 (expense); month defaults to the current month.
func summaryByCategoryHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}
		month := r.URL.Query().Get("month")
		if month == "" {
			month = currentMonthKey()
		}
		direction := -1
		if v := r.URL.Query().Get("direction"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && (n == 1 || n == -1) {
				direction = n
			}
		}
		slices, err := st.SummaryByCategory(u.ID, month, direction)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid month"})
			return
		}
		_ = json.NewEncoder(w).Encode(summaryByCategoryResponse{
			Month:     month,
			Direction: direction,
			Slices:    slices,
		})
	}
}

// updateEntryCategoryRequest is the body for PATCH /api/entries/{id}/category.
// CategoryID is a pointer so an explicit null clears the category (unclassify),
// while omitting it is treated the same as null.
type updateEntryCategoryRequest struct {
	CategoryID *int64 `json:"category_id"`
}

// entryItemHandler routes PATCH /api/entries/{id}/category (change category),
// PUT /api/entries/{id} (full-field edit), and DELETE /api/entries/{id} (soft
// delete). Session-authenticated; scoped to the caller's own entries.
func entryItemHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/api/entries/")
		// PATCH /api/entries/{id}/category routes separately.
		if idStr, ok := strings.CutSuffix(rest, "/category"); ok {
			if r.Method != http.MethodPatch {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil || id <= 0 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid entry id"})
				return
			}
			entryChangeCategoryHandler(st, w, r, u, id)
			return
		}

		// Otherwise it's PUT /api/entries/{id} or DELETE /api/entries/{id}.
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || id <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid entry id"})
			return
		}
		switch r.Method {
		case http.MethodPut:
			entryEditHandler(st, w, r, u, id)
		case http.MethodDelete:
			entryDeleteHandler(st, w, r, u, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// entryChangeCategoryHandler handles PATCH /api/entries/{id}/category: manually
// (re)assign or clear (unclassify) an entry's category.
func entryChangeCategoryHandler(st *store.Store, w http.ResponseWriter, r *http.Request, u *store.User, id int64) {
	var req updateEntryCategoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
		return
	}
	if err := st.UpdateEntryCategory(u.ID, id, req.CategoryID); err != nil {
		switch err {
		case store.ErrEntryNotFound:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "entry not found"})
		case store.ErrCategoryNotFound:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "category not found"})
		default:
			log.Printf("update entry category error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
		}
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

// entryEditHandler handles PUT /api/entries/{id}: full-field edit (amount,
// summary, time, note, category).
func entryEditHandler(st *store.Store, w http.ResponseWriter, r *http.Request, u *store.User, id int64) {
	var req manualEntryRequest
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
		return
	}
	in, msg := parseManualEntry(req)
	if msg != "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
		return
	}
	if err := st.UpdateEntry(u.ID, id, in); err != nil {
		switch err {
		case store.ErrEntryNotFound:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "entry not found"})
		case store.ErrCategoryNotFound:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "category not found"})
		case store.ErrInvalidAmount, store.ErrInvalidEntry:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: err.Error()})
		default:
			log.Printf("update entry error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
		}
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

// entryDeleteHandler handles DELETE /api/entries/{id}: soft delete (mark
// deleted_at, hide from lists/summaries).
func entryDeleteHandler(st *store.Store, w http.ResponseWriter, r *http.Request, u *store.User, id int64) {
	if err := st.SoftDeleteEntry(u.ID, id); err != nil {
		if err == store.ErrEntryNotFound {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "entry not found"})
			return
		}
		log.Printf("soft delete entry error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

// categoryRequest is the body for creating/updating a category.
type categoryRequest struct {
	ParentID  *int64 `json:"parent_id"`
	Name      string `json:"name"`
	Direction int    `json:"direction"` // 1 income / -1 expense (ignored on update, inherited when parent set)
	Regex     string `json:"regex"`
	SortOrder int    `json:"sort_order"`
	Move      bool   `json:"move"` // when true on PUT, reparent to ParentID (nil = root)
}

// categoriesHandler serves GET (list) and POST (create) on /api/categories.
func categoriesHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}

		switch r.Method {
		case http.MethodGet:
			cats, err := st.ListCategories(u.ID)
			if err != nil {
				log.Printf("list categories error: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"categories": cats})

		case http.MethodPost:
			var req categoryRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
				return
			}
			if req.Name == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "name is required"})
				return
			}
			if req.ParentID == nil && req.Direction != 1 && req.Direction != -1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "direction must be 1 or -1 for a root category"})
				return
			}
			id, err := st.CreateCategory(u.ID, req.ParentID, req.Name, req.Direction, req.SortOrder, req.Regex)
			if err != nil {
				switch err {
				case store.ErrCategoryNotFound:
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "parent category not found"})
				case store.ErrMaxDepth:
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "category depth exceeds 5 levels"})
				default:
					log.Printf("create category error: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
				}
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "id": id})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// categoryItemHandler serves PUT (update) and DELETE on /api/categories/{id}.
func categoryItemHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}

		idStr := strings.TrimPrefix(r.URL.Path, "/api/categories/")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid category id"})
			return
		}

		switch r.Method {
		case http.MethodPut:
			var req categoryRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
				return
			}
			if req.Name == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "name is required"})
				return
			}
			if err := st.UpdateCategory(u.ID, id, req.Name, req.SortOrder, req.Regex); err != nil {
				if err == store.ErrCategoryNotFound {
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "category not found"})
					return
				}
				if err == store.ErrInvalidRegex {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid regex pattern"})
					return
				}
				log.Printf("update category error: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
				return
			}
			// Optionally reparent (move subtree) in the same request.
			if req.Move {
				if err := st.MoveCategory(u.ID, id, req.ParentID); err != nil {
					switch err {
					case store.ErrCategoryNotFound:
						w.WriteHeader(http.StatusBadRequest)
						_ = json.NewEncoder(w).Encode(errorResponse{Error: "parent category not found"})
					case store.ErrInvalidMove:
						w.WriteHeader(http.StatusBadRequest)
						_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid move: cycle or direction mismatch"})
					case store.ErrMaxDepth:
						w.WriteHeader(http.StatusBadRequest)
						_ = json.NewEncoder(w).Encode(errorResponse{Error: "category depth exceeds 5 levels"})
					default:
						log.Printf("move category error: %v", err)
						w.WriteHeader(http.StatusInternalServerError)
						_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
					}
					return
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})

		case http.MethodDelete:
			if err := st.DeleteCategory(u.ID, id); err != nil {
				switch err {
				case store.ErrCategoryHasChildren:
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "category has children"})
				case store.ErrCategoryNotFound:
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "category not found"})
				default:
					log.Printf("delete category error: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
				}
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// requireAdmin returns the current user if they are an authenticated admin.
// Otherwise it writes the appropriate error (401 unauthenticated / 403
// non-admin) and returns nil, so callers can just `return` on nil.
func requireAdmin(st *store.Store, w http.ResponseWriter, r *http.Request) *store.User {
	u := currentUser(st, r)
	if u == nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
		return nil
	}
	if !u.IsAdmin {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: "forbidden: admin only"})
		return nil
	}
	return u
}

// createUserRequest is the body for POST /api/users.
type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// resetPasswordRequest is the body for POST /api/users/{id}/reset-password.
type resetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// usersHandler serves GET (list) and POST (create) on /api/users. Admin only.
func usersHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if requireAdmin(st, w, r) == nil {
			return
		}
		switch r.Method {
		case http.MethodGet:
			users, err := st.ListUsers()
			if err != nil {
				log.Printf("list users error: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"users": users})

		case http.MethodPost:
			var req createUserRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
				return
			}
			req.Username = strings.TrimSpace(req.Username)
			if req.Username == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "username is required"})
				return
			}
			if len(req.Password) < minPasswordLen {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "password too short"})
				return
			}
			id, err := st.CreateUser(req.Username, req.Password)
			if err != nil {
				if err == store.ErrUsernameTaken {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "username already taken"})
					return
				}
				log.Printf("create user error: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "id": id, "username": req.Username})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// userItemHandler serves DELETE /api/users/{id} and
// POST /api/users/{id}/reset-password. Admin only.
func userItemHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if requireAdmin(st, w, r) == nil {
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/api/users/")
		idStr := rest
		isReset := false
		if strings.HasSuffix(rest, "/reset-password") {
			idStr = strings.TrimSuffix(rest, "/reset-password")
			isReset = true
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid user id"})
			return
		}

		if isReset {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var req resetPasswordRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
				return
			}
			if len(req.NewPassword) < minPasswordLen {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "password too short"})
				return
			}
			if err := st.AdminResetPassword(id, req.NewPassword); err != nil {
				if err == store.ErrUserNotFound {
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(errorResponse{Error: "user not found"})
					return
				}
				log.Printf("reset password error: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			return
		}

		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := st.DeleteUser(id); err != nil {
			switch err {
			case store.ErrCannotDeleteAdmin:
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "cannot delete an administrator account"})
			case store.ErrUserNotFound:
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "user not found"})
			default:
				log.Printf("delete user error: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	resp := healthResponse{
		Status:  "ok",
		Service: "almanac",
		Time:    time.Now().In(cstZone).Format(time.RFC3339),
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

// dbCheckHandler records a visit and returns the running total. It validates
// that SQLite works and that data persists across deployments.
func dbCheckHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		count, err := st.RecordVisit(time.Now().In(cstZone))
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			log.Printf("db-check error: %v", err)
			return
		}
		resp := dbCheckResponse{
			Status: "ok",
			Visits: count,
			Time:   time.Now().In(cstZone).Format(time.RFC3339),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "failed to encode response", http.StatusInternalServerError)
		}
	}
}

// spaFileSystem wraps an fs.FS to provide a Fallback to 404.html when a file
// is not found, enabling pretty 404 pages in the embedded frontend.
type spaFileSystem struct {
	fs fs.FS
}

func (s spaFileSystem) Open(name string) (fs.File, error) {
	f, err := s.fs.Open(name)
	if err == nil {
		return f, nil
	}
	// If the file is not found, try returning 404.html from the root.
	if os.IsNotExist(err) || strings.Contains(err.Error(), "not found") {
		f404, err404 := s.fs.Open("404.html")
		if err404 == nil {
			return f404, nil
		}
	}
	return nil, err
}

func main() {
	// Listen address is configurable via flag or ADDR env var; defaults to :8080.
	addr := flag.String("addr", "", "HTTP listen address, e.g. :8080")
	dbPath := flag.String("db", "", "SQLite database file path")
	flag.Parse()

	listen := *addr
	if listen == "" {
		listen = os.Getenv("ADDR")
	}
	if listen == "" {
		listen = ":8080"
	}

	dbFile := *dbPath
	if dbFile == "" {
		dbFile = os.Getenv("DB_PATH")
	}
	if dbFile == "" {
		dbFile = "data/almanac.db"
	}

	st, err := store.Open(dbFile)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer st.Close()
	log.Printf("database ready at %s", dbFile)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/db-check", dbCheckHandler(st))
	mux.Handle("/api/login", loginHandler(st))
	mux.Handle("/api/logout", logoutHandler(st))
	mux.Handle("/api/whoami", whoamiHandler(st))
	mux.Handle("/api/change-password", changePasswordHandler(st))
	mux.Handle("/api/webhook/entry", webhookHandler(st))
	mux.Handle("/api/webhook-token", webhookTokenHandler(st))
	mux.Handle("/api/webhook-token/reset", webhookTokenResetHandler(st))
	mux.Handle("/api/entries", entriesHandler(st))
	mux.Handle("/api/entries/", entryItemHandler(st))
	mux.Handle("/api/summary", summaryHandler(st))
	mux.Handle("/api/summary/by-category", summaryByCategoryHandler(st))
	mux.Handle("/api/categories", categoriesHandler(st))
	mux.Handle("/api/categories/", categoryItemHandler(st))
	mux.Handle("/api/users", usersHandler(st))
	mux.Handle("/api/users/", userItemHandler(st))

	// Serve the embedded frontend (Astro build output) at the root path.
	// staticFS is provided by the build-tagged files (embed_dist.go /
	// embed_stub.go) so the server works with or without a built frontend.
	if sub, err := fs.Sub(staticFS, staticRoot); err == nil {
		mux.Handle("/", http.FileServer(http.FS(spaFileSystem{fs: sub})))
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("almanac starting, listening on %s", listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}
