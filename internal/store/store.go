// Package store handles SQLite persistence for Almanac.
//
// MVP stage: it only manages a "visits" table used to validate that the
// pure-Go SQLite driver works after cross-compilation and that data survives
// across deployments.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// ErrUserNotFound is returned when a username lookup finds no matching row.
var ErrUserNotFound = errors.New("user not found")

// ErrWrongPassword is returned when a supplied password does not match.
var ErrWrongPassword = errors.New("wrong password")

// ErrCategoryHasChildren is returned when deleting a category that still has
// child categories (parent_id ON DELETE RESTRICT).
var ErrCategoryHasChildren = errors.New("category has children")

// ErrCategoryNotFound is returned when a category lookup finds no matching row
// for the given user.
var ErrCategoryNotFound = errors.New("category not found")

// ErrMaxDepth is returned when creating a category would exceed level 5.
var ErrMaxDepth = errors.New("category depth exceeds 5 levels")

// ErrInvalidRegex is returned when a category's regex fails to compile. The
// pattern is validated at save time so the routing engine never has to deal
// with an uncompilable rule.
var ErrInvalidRegex = errors.New("invalid regex pattern")

// ErrEntryNotFound is returned when an entry lookup finds no matching row for
// the given user.
var ErrEntryNotFound = errors.New("entry not found")

// ErrInvalidMove is returned when a category move is illegal: moving a node
// under itself or one of its own descendants (a cycle), or under a parent of a
// different direction (direction is immutable and must be inherited).
var ErrInvalidMove = errors.New("invalid category move")

// ErrUsernameTaken is returned when creating a user whose username already exists.
var ErrUsernameTaken = errors.New("username already taken")

// ErrCannotDeleteAdmin is returned when attempting to delete an admin account.
var ErrCannotDeleteAdmin = errors.New("cannot delete an administrator account")

// User represents an application account. In the MVP every logged-in user is
// effectively an administrator of their own ledger.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	WebhookToken string
	IsAdmin      bool
	CreatedAt    string
}

// Store wraps the database handle.
type Store struct {
	db *sql.DB

	// rules is the per-user compiled routing rule cache (see router.go).
	// Lazily built on first classification and invalidated on any category
	// mutation for that user.
	rulesMu sync.RWMutex
	rules   map[int64]*compiledRuleSet
}

// Open opens (or creates) the SQLite database at the given path, ensuring the
// parent directory exists and the schema is applied.
func Open(dbPath string) (*Store, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	// Enforce foreign-key constraints on every connection in the pool.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable foreign_keys: %w", err)
	}

	s := &Store{db: db, rules: make(map[int64]*compiledRuleSet)}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := s.seedAdmin(); err != nil {
		return nil, err
	}
	if err := s.backfillDefaultLedgers(); err != nil {
		return nil, err
	}
	return s, nil
}

// migrate applies the (tiny) MVP schema.
func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS visits (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    time TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    webhook_token TEXT NOT NULL UNIQUE,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ledgers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL DEFAULT '默认账本',
    is_default INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_default_ledger ON ledgers(user_id) WHERE is_default = 1;

CREATE TABLE IF NOT EXISTS categories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id  INTEGER REFERENCES categories(id) ON DELETE RESTRICT,
    name       TEXT NOT NULL,
    direction  INTEGER NOT NULL CHECK(direction IN (-1, 1)),
    level      INTEGER NOT NULL CHECK(level BETWEEN 1 AND 5),
    regex      TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cat_user_parent ON categories(user_id, parent_id);
CREATE INDEX IF NOT EXISTS idx_cat_user_sort ON categories(user_id, sort_order);

CREATE TABLE IF NOT EXISTS ledger_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ledger_id   INTEGER NOT NULL REFERENCES ledgers(id) ON DELETE CASCADE,
    category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL,
    amount_cents INTEGER NOT NULL CHECK(amount_cents != 0),
    raw_type    TEXT NOT NULL,
    record_time TEXT NOT NULL,
    note        TEXT,
    source      TEXT NOT NULL DEFAULT 'webhook' CHECK(source IN ('webhook','manual','csv')),
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entry_user_time ON ledger_entries(user_id, record_time);
CREATE INDEX IF NOT EXISTS idx_entry_user_cat ON ledger_entries(user_id, category_id);

CREATE TRIGGER IF NOT EXISTS trg_cat_dir_ins BEFORE INSERT ON categories
WHEN NEW.parent_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT user_id FROM categories WHERE id = NEW.parent_id) <> NEW.user_id
            THEN RAISE(ABORT, 'parent category must belong to the same user')
        WHEN (SELECT direction FROM categories WHERE id = NEW.parent_id) <> NEW.direction
            THEN RAISE(ABORT, 'direction must inherit from parent')
    END;
END;

CREATE TRIGGER IF NOT EXISTS trg_cat_dir_upd BEFORE UPDATE ON categories
WHEN NEW.parent_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT user_id FROM categories WHERE id = NEW.parent_id) <> NEW.user_id
            THEN RAISE(ABORT, 'parent category must belong to the same user')
        WHEN (SELECT direction FROM categories WHERE id = NEW.parent_id) <> NEW.direction
            THEN RAISE(ABORT, 'direction must inherit from parent')
    END;
END;

CREATE TRIGGER IF NOT EXISTS trg_cat_dir_immutable BEFORE UPDATE OF direction ON categories
WHEN OLD.direction <> NEW.direction
BEGIN
    SELECT RAISE(ABORT, 'direction is immutable');
END;`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := s.migrateIsAdmin(); err != nil {
		return err
	}
	if err := s.migrateAmountAbs(); err != nil {
		return err
	}
	return nil
}

// migrateAmountAbs converts any legacy signed amount_cents rows to their
// absolute (unsigned) value. Direction is no longer carried by the amount sign
// (it is derived from the entry's category), so amounts are stored unsigned.
// Legacy negative rows were expenses filed under expense categories, so taking
// abs() preserves their meaning. Idempotent: a no-op once all rows are >= 0.
// The schema CHECK stays `!= 0` (not `> 0`) so we avoid an SQLite table rebuild;
// after this migration no negative rows remain anyway.
func (s *Store) migrateAmountAbs() error {
	res, err := s.db.Exec("UPDATE ledger_entries SET amount_cents = abs(amount_cents), updated_at = updated_at WHERE amount_cents < 0")
	if err != nil {
		return fmt.Errorf("migrate amount to unsigned: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("migrated %d ledger entries to unsigned amount_cents", n)
	}
	return nil
}

// migrateIsAdmin adds the users.is_admin column on databases created before
// the account-management feature existed, then flags the legacy 'admin'
// account as the administrator. Idempotent: a no-op once the column is present.
func (s *Store) migrateIsAdmin() error {
	rows, err := s.db.Query("PRAGMA table_info(users)")
	if err != nil {
		return fmt.Errorf("inspect users columns: %w", err)
	}
	defer rows.Close()
	hasCol := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan column info: %w", err)
		}
		if name == "is_admin" {
			hasCol = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate column info: %w", err)
	}
	if hasCol {
		return nil
	}
	if _, err := s.db.Exec("ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("add is_admin column: %w", err)
	}
	if _, err := s.db.Exec("UPDATE users SET is_admin = 1 WHERE username = 'admin'"); err != nil {
		return fmt.Errorf("flag admin user: %w", err)
	}
	return nil
}

// randomToken returns a cryptographically-random hex string of n bytes.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// seedAdmin creates the default super-admin account on first launch. It is a
// no-op once any user exists. The default credentials are logged with a loud
// warning so the operator changes them promptly.
func (s *Store) seedAdmin() error {
	var count int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count > 0 {
		return nil
	}

	const defaultUser = "admin"
	const defaultPass = "almanac@2026"
	hash, err := bcrypt.GenerateFromPassword([]byte(defaultPass), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash default password: %w", err)
	}
	token, err := randomToken(24)
	if err != nil {
		return fmt.Errorf("generate webhook token: %w", err)
	}
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(
		"INSERT INTO users (username, password_hash, webhook_token, is_admin, created_at) VALUES (?, ?, ?, 1, ?)",
		defaultUser, string(hash), token, now,
	)
	if err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	userID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get admin id: %w", err)
	}
	// Every user starts with one default ledger.
	if _, err := s.db.Exec(
		"INSERT INTO ledgers (user_id, name, is_default, created_at, updated_at) VALUES (?, '默认账本', 1, ?, ?)",
		userID, now, now,
	); err != nil {
		return fmt.Errorf("seed default ledger: %w", err)
	}
	log.Printf("WARNING: seeded default admin account username=%q password=%q -- CHANGE THIS PASSWORD ASAP", defaultUser, defaultPass)
	return nil
}

// RecordVisit inserts one visit row and returns the total visit count.
func (s *Store) RecordVisit(now time.Time) (int64, error) {
	if _, err := s.db.Exec("INSERT INTO visits (time) VALUES (?)", now.Format(time.RFC3339)); err != nil {
		return 0, fmt.Errorf("insert visit: %w", err)
	}
	var count int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM visits").Scan(&count); err != nil {
		return 0, fmt.Errorf("count visits: %w", err)
	}
	return count, nil
}

// backfillDefaultLedgers ensures every user has a default ledger. It repairs
// accounts created before the ledgers table existed (e.g. the original admin
// seeded at an earlier schema version), and is a harmless no-op otherwise.
func (s *Store) backfillDefaultLedgers() error {
	rows, err := s.db.Query(`
SELECT u.id FROM users u
WHERE NOT EXISTS (SELECT 1 FROM ledgers l WHERE l.user_id = u.id AND l.is_default = 1)`)
	if err != nil {
		return fmt.Errorf("scan users without default ledger: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan user id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate users: %w", err)
	}
	now := time.Now().Format(time.RFC3339)
	for _, id := range ids {
		if _, err := s.db.Exec(
			"INSERT INTO ledgers (user_id, name, is_default, created_at, updated_at) VALUES (?, '默认账本', 1, ?, ?)",
			id, now, now,
		); err != nil {
			return fmt.Errorf("backfill default ledger for user %d: %w", id, err)
		}
		log.Printf("backfilled default ledger for user %d", id)
	}
	return nil
}

// UserByUsername looks up a user by username. Returns ErrUserNotFound if none.
func (s *Store) UserByUsername(username string) (*User, error) {
	var u User
	err := s.db.QueryRow(
		"SELECT id, username, password_hash, webhook_token, is_admin, created_at FROM users WHERE username = ?",
		username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.WebhookToken, &u.IsAdmin, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}
	return &u, nil
}

// VerifyLogin checks the username/password pair and, on success, returns the
// user. It returns ErrUserNotFound for both unknown users and wrong passwords
// so callers cannot distinguish the two (avoids username enumeration).
func (s *Store) VerifyLogin(username, password string) (*User, error) {
	u, err := s.UserByUsername(username)
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, ErrUserNotFound
	}
	return u, nil
}

// CreateSession issues a new session token for the user, valid for ttl.
func (s *Store) CreateSession(userID int64, ttl time.Duration) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	now := time.Now()
	if _, err := s.db.Exec(
		"INSERT INTO sessions (token, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)",
		token, userID, now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339),
	); err != nil {
		return "", fmt.Errorf("insert session: %w", err)
	}
	return token, nil
}

// UserBySession resolves a session token to its user, enforcing expiry.
// Expired sessions are treated as absent (and lazily deleted).
func (s *Store) UserBySession(token string) (*User, error) {
	var (
		u         User
		expiresAt string
	)
	err := s.db.QueryRow(`
SELECT u.id, u.username, u.password_hash, u.webhook_token, u.is_admin, u.created_at, s.expires_at
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token = ?`, token).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.WebhookToken, &u.IsAdmin, &u.CreatedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}
	exp, perr := time.Parse(time.RFC3339, expiresAt)
	if perr != nil || time.Now().After(exp) {
		_ = s.DeleteSession(token)
		return nil, ErrUserNotFound
	}
	return &u, nil
}

// DeleteSession removes a session token (logout).
func (s *Store) DeleteSession(token string) error {
	if _, err := s.db.Exec("DELETE FROM sessions WHERE token = ?", token); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ChangePassword verifies the user's current password and, on success,
// replaces it with a new bcrypt hash. It returns ErrUserNotFound if the user
// does not exist and ErrWrongPassword if the old password does not match.
func (s *Store) ChangePassword(userID int64, oldPassword, newPassword string) error {
	var hash string
	err := s.db.QueryRow("SELECT password_hash FROM users WHERE id = ?", userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("query user: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)) != nil {
		return ErrWrongPassword
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash new password: %w", err)
	}
	if _, err := s.db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", string(newHash), userID); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

// DeleteUserSessions removes all sessions for a user (e.g. after a password
// change), forcing re-login everywhere.
func (s *Store) DeleteUserSessions(userID int64) error {
	if _, err := s.db.Exec("DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// UserByWebhookToken looks up a user by their webhook token.
func (s *Store) UserByWebhookToken(token string) (*User, error) {
	var u User
	err := s.db.QueryRow(
		"SELECT id, username, password_hash, webhook_token, is_admin, created_at FROM users WHERE webhook_token = ?",
		token,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.WebhookToken, &u.IsAdmin, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query user by token: %w", err)
	}
	return &u, nil
}

// UserInfo is a user summary for the admin account-management list (no secrets).
type UserInfo struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	IsAdmin   bool   `json:"is_admin"`
	CreatedAt string `json:"created_at"`
}

// ListUsers returns all accounts (admin-only view), oldest first.
func (s *Store) ListUsers() ([]UserInfo, error) {
	rows, err := s.db.Query("SELECT id, username, is_admin, created_at FROM users ORDER BY id ASC")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	users := make([]UserInfo, 0, 8)
	for rows.Next() {
		var u UserInfo
		if err := rows.Scan(&u.ID, &u.Username, &u.IsAdmin, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return users, nil
}

// CreateUser provisions a new non-admin account: bcrypt-hashed password, a
// random webhook token, and a default ledger. Returns ErrUsernameTaken if the
// username collides.
func (s *Store) CreateUser(username, password string) (int64, error) {
	if _, err := s.UserByUsername(username); err == nil {
		return 0, ErrUsernameTaken
	} else if !errors.Is(err, ErrUserNotFound) {
		return 0, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, fmt.Errorf("hash password: %w", err)
	}
	token, err := randomToken(24)
	if err != nil {
		return 0, fmt.Errorf("generate webhook token: %w", err)
	}
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(
		"INSERT INTO users (username, password_hash, webhook_token, is_admin, created_at) VALUES (?, ?, ?, 0, ?)",
		username, string(hash), token, now,
	)
	if err != nil {
		return 0, fmt.Errorf("insert user: %w", err)
	}
	userID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get user id: %w", err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO ledgers (user_id, name, is_default, created_at, updated_at) VALUES (?, '\u9ed8\u8ba4\u8d26\u672c', 1, ?, ?)",
		userID, now, now,
	); err != nil {
		return 0, fmt.Errorf("seed default ledger: %w", err)
	}
	return userID, nil
}

// DeleteUser removes an account and all its data (ON DELETE CASCADE). It
// refuses to delete any administrator account (guards against self-deletion
// and losing the last admin), returning ErrCannotDeleteAdmin.
func (s *Store) DeleteUser(id int64) error {
	var isAdmin bool
	err := s.db.QueryRow("SELECT is_admin FROM users WHERE id = ?", id).Scan(&isAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}
	if isAdmin {
		return ErrCannotDeleteAdmin
	}
	if _, err := s.db.Exec("DELETE FROM users WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

// AdminResetPassword sets a new password for any user (no old-password check)
// and revokes all of that user's active sessions, forcing re-login.
func (s *Store) AdminResetPassword(id int64, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	res, err := s.db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", string(hash), id)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return s.DeleteUserSessions(id)
}

// DefaultLedgerID returns the id of the user's default ledger.
func (s *Store) DefaultLedgerID(userID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		"SELECT id FROM ledgers WHERE user_id = ? AND is_default = 1",
		userID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("no default ledger for user %d", userID)
	}
	if err != nil {
		return 0, fmt.Errorf("query default ledger: %w", err)
	}
	return id, nil
}

// Entry represents one ledger record for insertion.
type Entry struct {
	UserID     int64
	LedgerID   int64
	CategoryID *int64 // nil = unclassified
	AmountCents int64
	RawType    string
	RecordTime string // "YYYY-MM-DD HH:mm"
	Note       string
	Source     string // webhook / manual / csv
}

// InsertEntry inserts one ledger entry and returns its new id.
func (s *Store) InsertEntry(e Entry) (int64, error) {
	now := time.Now().Format(time.RFC3339)
	var categoryID any
	if e.CategoryID != nil {
		categoryID = *e.CategoryID
	}
	var note any
	if e.Note != "" {
		note = e.Note
	}
	res, err := s.db.Exec(`
INSERT INTO ledger_entries
    (user_id, ledger_id, category_id, amount_cents, raw_type, record_time, note, source, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.UserID, e.LedgerID, categoryID, e.AmountCents, e.RawType, e.RecordTime, note, e.Source, now, now)
	if err != nil {
		return 0, fmt.Errorf("insert entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get entry id: %w", err)
	}
	return id, nil
}

// EntryRow is one ledger entry as shown in list views. CategoryName is empty
// when the entry is still unclassified (category_id IS NULL). Direction is the
// entry's derived direction (1 income / -1 expense), taken from the assigned
// category; it is 0 for an unclassified entry ("待分类", no direction). Amounts
// are stored unsigned (absolute cents); direction is NOT carried by the sign.
type EntryRow struct {
	ID           int64   `json:"id"`
	AmountCents  int64   `json:"amount_cents"`
	RawType      string  `json:"raw_type"`
	RecordTime   string  `json:"record_time"`
	Note         string  `json:"note"`
	Source       string  `json:"source"`
	CategoryID   *int64  `json:"category_id"`
	CategoryName string  `json:"category_name"`
	CategoryPath string  `json:"category_path"`
	Direction    int     `json:"direction"` // 1 income / -1 expense / 0 unclassified
}

// ListEntries returns a page of the user's ledger entries, newest first
// (by record_time then id). limit is clamped to [1,200]; offset is floored
// at 0. It also returns the total row count for pagination.
func (s *Store) ListEntries(userID int64, limit, offset int) ([]EntryRow, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var total int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM ledger_entries WHERE user_id = ?", userID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count entries: %w", err)
	}

	rows, err := s.db.Query(`
SELECT e.id, e.amount_cents, e.raw_type, e.record_time,
       COALESCE(e.note, ''), e.source, e.category_id, COALESCE(c.name, ''),
       COALESCE(c.direction, 0)
FROM ledger_entries e
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.user_id = ?
ORDER BY e.record_time DESC, e.id DESC
LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list entries: %w", err)
	}
	defer rows.Close()

	entries := make([]EntryRow, 0, limit)
	for rows.Next() {
		var e EntryRow
		if err := rows.Scan(&e.ID, &e.AmountCents, &e.RawType, &e.RecordTime,
			&e.Note, &e.Source, &e.CategoryID, &e.CategoryName, &e.Direction); err != nil {
			return nil, 0, fmt.Errorf("scan entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate entries: %w", err)
	}

	// Enrich classified rows with the full category path (餐饮>饮品>咖啡) reusing
	// the per-user cached category-tree snapshot; O(depth<=5) per row, no extra
	// DB round-trips. A snapshot error is non-fatal: paths just stay empty.
	if set, err := s.rulesFor(userID); err == nil {
		for i := range entries {
			if entries[i].CategoryID != nil {
				entries[i].CategoryPath = set.pathOf(*entries[i].CategoryID)
			}
		}
	}
	return entries, total, nil
}

// UpdateEntryCategory manually (re)assigns or clears the category of one of the
// user's entries. Pass categoryID == nil to unclassify. When a category is
// given it must belong to the same user; its direction is NOT validated against
// the amount (amounts are unsigned and direction is derived from the assigned
// category), so a category of any direction may be attached to any entry. The
// entry itself must belong to the user, otherwise ErrEntryNotFound.
func (s *Store) UpdateEntryCategory(userID, entryID int64, categoryID *int64) error {
	// Confirm the entry exists and is owned by the user.
	var amountCents int64
	err := s.db.QueryRow(
		"SELECT amount_cents FROM ledger_entries WHERE id = ? AND user_id = ?",
		entryID, userID,
	).Scan(&amountCents)
	if err == sql.ErrNoRows {
		return ErrEntryNotFound
	}
	if err != nil {
		return fmt.Errorf("lookup entry: %w", err)
	}

	if categoryID != nil {
		// Confirm the category exists and is owned by the user. Direction is
		// no longer checked against the amount sign.
		var dummy int
		err := s.db.QueryRow(
			"SELECT 1 FROM categories WHERE id = ? AND user_id = ?",
			*categoryID, userID,
		).Scan(&dummy)
		if err == sql.ErrNoRows {
			return ErrCategoryNotFound
		}
		if err != nil {
			return fmt.Errorf("lookup category: %w", err)
		}
	}

	var catVal any
	if categoryID != nil {
		catVal = *categoryID
	}
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(
		"UPDATE ledger_entries SET category_id = ?, updated_at = ? WHERE id = ? AND user_id = ?",
		catVal, now, entryID, userID,
	)
	if err != nil {
		return fmt.Errorf("update entry category: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrEntryNotFound
	}
	return nil
}

// Category is one node in a user's category tree.
type Category struct {
	ID        int64  `json:"id"`
	ParentID  *int64 `json:"parent_id"`
	Name      string `json:"name"`
	Direction int    `json:"direction"` // 1 income / -1 expense
	Level     int    `json:"level"`
	Regex     string `json:"regex"`
	SortOrder int    `json:"sort_order"`
}

// ListCategories returns all of the user's categories ordered for tree
// rendering (by level, then sort_order, then id).
func (s *Store) ListCategories(userID int64) ([]Category, error) {
	rows, err := s.db.Query(`
SELECT id, parent_id, name, direction, level, COALESCE(regex, ''), sort_order
FROM categories
WHERE user_id = ?
ORDER BY level ASC, sort_order ASC, id ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	cats := make([]Category, 0, 16)
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.ParentID, &c.Name, &c.Direction, &c.Level, &c.Regex, &c.SortOrder); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		cats = append(cats, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate categories: %w", err)
	}
	return cats, nil
}

// CreateCategory inserts a new category. level is derived from the parent:
// root nodes are level 1; a child is parent.level+1 (rejected past 5). When
// parentID is set, direction is forced to match the parent's (DB triggers also
// enforce this and same-user ownership).
func (s *Store) CreateCategory(userID int64, parentID *int64, name string, direction, sortOrder int, regex string) (int64, error) {
	if err := validateRegex(regex); err != nil {
		return 0, err
	}
	level := 1
	if parentID != nil {
		var pLevel, pDir int
		err := s.db.QueryRow(
			"SELECT level, direction FROM categories WHERE id = ? AND user_id = ?",
			*parentID, userID,
		).Scan(&pLevel, &pDir)
		if err == sql.ErrNoRows {
			return 0, ErrCategoryNotFound
		}
		if err != nil {
			return 0, fmt.Errorf("lookup parent category: %w", err)
		}
		if pLevel >= 5 {
			return 0, ErrMaxDepth
		}
		level = pLevel + 1
		direction = pDir // inherit
	}

	var regexVal any
	if regex != "" {
		regexVal = regex
	}
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(`
INSERT INTO categories (user_id, parent_id, name, direction, level, regex, sort_order, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, parentID, name, direction, level, regexVal, sortOrder, now, now)
	if err != nil {
		return 0, fmt.Errorf("insert category: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get category id: %w", err)
	}
	s.InvalidateRules(userID)
	return id, nil
}

// UpdateCategory changes the mutable fields (name, regex, sort_order) of a
// user's category. direction/parent_id/level are immutable here by design.
func (s *Store) UpdateCategory(userID, id int64, name string, sortOrder int, regex string) error {
	if err := validateRegex(regex); err != nil {
		return err
	}
	var regexVal any
	if regex != "" {
		regexVal = regex
	}
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(`
UPDATE categories SET name = ?, regex = ?, sort_order = ?, updated_at = ?
WHERE id = ? AND user_id = ?`,
		name, regexVal, sortOrder, now, id, userID)
	if err != nil {
		return fmt.Errorf("update category: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrCategoryNotFound
	}
	s.InvalidateRules(userID)
	return nil
}

// MoveCategory reparents a category under newParentID (nil = make it a root),
// recomputing its level and cascading the level shift to all descendants. The
// move is rejected (ErrInvalidMove) when it would create a cycle (moving under
// itself or a descendant) or cross directions (a different-direction parent).
// ErrMaxDepth is returned when the resulting subtree would exceed level 5.
// Runs in a single transaction.
func (s *Store) MoveCategory(userID, id int64, newParentID *int64) error {
	// Load the whole tree for this user once; validate and compute in memory.
	cats, err := s.ListCategories(userID)
	if err != nil {
		return fmt.Errorf("load categories: %w", err)
	}
	byID := make(map[int64]Category, len(cats))
	children := make(map[int64][]int64)
	for _, c := range cats {
		byID[c.ID] = c
		if c.ParentID != nil {
			children[*c.ParentID] = append(children[*c.ParentID], c.ID)
		}
	}
	node, ok := byID[id]
	if !ok {
		return ErrCategoryNotFound
	}

	newLevel := 1
	if newParentID != nil {
		if *newParentID == id {
			return ErrInvalidMove // cannot parent to self
		}
		parent, ok := byID[*newParentID]
		if !ok {
			return ErrCategoryNotFound
		}
		if parent.Direction != node.Direction {
			return ErrInvalidMove // direction is immutable; parent must match
		}
		// Reject moving under one of the node's own descendants (cycle).
		if isDescendant(children, id, *newParentID) {
			return ErrInvalidMove
		}
		newLevel = parent.Level + 1
	}

	// Depth check: newLevel + (subtree height - 1) must stay <= 5.
	if newLevel+subtreeHeight(children, byID, id)-1 > 5 {
		return ErrMaxDepth
	}

	delta := newLevel - node.Level
	if delta == 0 && sameParent(node.ParentID, newParentID) {
		return nil // no-op
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin move tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().Format(time.RFC3339)
	var parentVal any
	if newParentID != nil {
		parentVal = *newParentID
	}
	if _, err := tx.Exec(
		"UPDATE categories SET parent_id = ?, level = ?, updated_at = ? WHERE id = ? AND user_id = ?",
		parentVal, newLevel, now, id, userID,
	); err != nil {
		return fmt.Errorf("move node: %w", err)
	}
	// Shift every descendant's level by the same delta.
	if delta != 0 {
		for _, descID := range collectDescendants(children, id) {
			if _, err := tx.Exec(
				"UPDATE categories SET level = level + ?, updated_at = ? WHERE id = ? AND user_id = ?",
				delta, now, descID, userID,
			); err != nil {
				return fmt.Errorf("shift descendant level: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit move: %w", err)
	}
	s.InvalidateRules(userID)
	return nil
}

// isDescendant reports whether target is in the subtree rooted at ancestor.
func isDescendant(children map[int64][]int64, ancestor, target int64) bool {
	for _, c := range children[ancestor] {
		if c == target || isDescendant(children, c, target) {
			return true
		}
	}
	return false
}

// collectDescendants returns all ids in the subtree below root (excluding root).
func collectDescendants(children map[int64][]int64, root int64) []int64 {
	var out []int64
	for _, c := range children[root] {
		out = append(out, c)
		out = append(out, collectDescendants(children, c)...)
	}
	return out
}

// subtreeHeight returns the number of levels in the subtree rooted at id
// (a leaf has height 1).
func subtreeHeight(children map[int64][]int64, byID map[int64]Category, id int64) int {
	max := 1
	for _, c := range children[id] {
		if h := 1 + subtreeHeight(children, byID, c); h > max {
			max = h
		}
	}
	return max
}

// sameParent reports whether two nullable parent ids are equal.
func sameParent(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// DeleteCategory removes a leaf category. If the category still has children
// it returns ErrCategoryHasChildren (parent_id is ON DELETE RESTRICT).
func (s *Store) DeleteCategory(userID, id int64) error {
	var childCount int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM categories WHERE parent_id = ? AND user_id = ?",
		id, userID,
	).Scan(&childCount); err != nil {
		return fmt.Errorf("count children: %w", err)
	}
	if childCount > 0 {
		return ErrCategoryHasChildren
	}
	res, err := s.db.Exec("DELETE FROM categories WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return fmt.Errorf("delete category: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrCategoryNotFound
	}
	s.InvalidateRules(userID)
	return nil
}

// RegenerateWebhookToken issues a fresh webhook token for the user and returns
// the new value. The old token immediately stops working.
func (s *Store) RegenerateWebhookToken(userID int64) (string, error) {
	token, err := randomToken(24)
	if err != nil {
		return "", fmt.Errorf("generate webhook token: %w", err)
	}
	if _, err := s.db.Exec("UPDATE users SET webhook_token = ? WHERE id = ?", token, userID); err != nil {
		return "", fmt.Errorf("update webhook token: %w", err)
	}
	return token, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
