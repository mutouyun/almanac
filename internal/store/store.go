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
);`
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
	if _, err := s.db.Exec(
		"INSERT INTO users (username, password_hash, webhook_token, created_at) VALUES (?, ?, ?, ?)",
		defaultUser, string(hash), token, time.Now().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("seed admin: %w", err)
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

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
