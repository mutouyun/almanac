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
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// ErrUserNotFound is returned when a username lookup finds no matching row.
var ErrUserNotFound = errors.New("user not found")

// ErrWrongPassword is returned when a supplied password does not match.
var ErrWrongPassword = errors.New("wrong password")

// User represents an application account. In the MVP every logged-in user is
// effectively an administrator of their own ledger.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	WebhookToken string
	CreatedAt    string
}

// Store wraps the database handle.
type Store struct {
	db *sql.DB
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

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := s.seedAdmin(); err != nil {
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
		"INSERT INTO users (username, password_hash, webhook_token, created_at) VALUES (?, ?, ?, ?)",
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

// UserByUsername looks up a user by username. Returns ErrUserNotFound if none.
func (s *Store) UserByUsername(username string) (*User, error) {
	var u User
	err := s.db.QueryRow(
		"SELECT id, username, password_hash, webhook_token, created_at FROM users WHERE username = ?",
		username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.WebhookToken, &u.CreatedAt)
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
SELECT u.id, u.username, u.password_hash, u.webhook_token, u.created_at, s.expires_at
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token = ?`, token).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.WebhookToken, &u.CreatedAt, &expiresAt)
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
		"SELECT id, username, password_hash, webhook_token, created_at FROM users WHERE webhook_token = ?",
		token,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.WebhookToken, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query user by token: %w", err)
	}
	return &u, nil
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
