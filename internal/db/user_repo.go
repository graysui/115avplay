package db

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"mediavault/internal/models"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters according to design §9.3: 64MiB memory, 3 iterations, 1 parallelism, 32 bytes salt, 32 bytes key.
const (
	argonMemory      = 64 * 1024 // 64 MB
	argonIterations  = 3
	argonParallelism = 1
	argonSaltLength  = 16
	argonKeyLength   = 32
)

// HashPassword hashes a plaintext password using Argon2id.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)

	// Format: $argon2id$v=19$m=65536,t=3,p=1$<b64salt>$<b64hash>
	encodedSalt := base64.RawStdEncoding.EncodeToString(salt)
	encodedHash := base64.RawStdEncoding.EncodeToString(hash)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonIterations, argonParallelism, encodedSalt, encodedHash), nil
}

// VerifyPassword verifies an Argon2id password hash.
func VerifyPassword(hashStr, password string) (bool, error) {
	parts := strings.Split(hashStr, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("invalid argon2id hash format")
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	_, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism)
	if err != nil {
		return false, fmt.Errorf("parse hash params: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}

	computedHash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expectedHash)))
	if subtle.ConstantTimeCompare(computedHash, expectedHash) == 1 {
		return true, nil
	}
	return false, nil
}

// UserRepo handles user and auth_sessions database operations.
type UserRepo struct {
	db *DB
}

func NewUserRepo(db *DB) *UserRepo {
	return &UserRepo{db: db}
}

// EnsureAdminUser checks if an admin exists, or creates the default admin user.
func (r *UserRepo) EnsureAdminUser(ctx context.Context, username, password string, mustChange bool) (*models.User, error) {
	var user models.User
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		row := database.QueryRowContext(ctx, `
			SELECT id, username, password_hash, is_admin, enabled, must_change_password, created_at, updated_at
			FROM users WHERE is_admin = 1 LIMIT 1
		`)
		return row.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.IsAdmin, &user.Enabled, &user.MustChangePassword, &user.CreatedAt, &user.UpdatedAt)
	})

	if err == nil {
		// Existing admin found
		return &user, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query admin user: %w", err)
	}

	// Admin not found, create new admin
	pwdHash, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash admin password: %w", err)
	}

	now := models.UTCNow()
	mustChangeInt := 0
	if mustChange {
		mustChangeInt = 1
	}

	admin := &models.User{
		ID:                 "usr_admin_" + base64.RawURLEncoding.EncodeToString([]byte(username)),
		Username:           username,
		PasswordHash:       pwdHash,
		IsAdmin:            1,
		Enabled:            1,
		MustChangePassword: mustChangeInt,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	err = r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO users (id, username, password_hash, is_admin, enabled, must_change_password, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, admin.ID, admin.Username, admin.PasswordHash, admin.IsAdmin, admin.Enabled, admin.MustChangePassword, admin.CreatedAt, admin.UpdatedAt)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("insert admin user: %w", err)
	}

	return admin, nil
}

// GetUserByUsername retrieves a user by username.
func (r *UserRepo) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	var u models.User
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		row := database.QueryRowContext(ctx, `
			SELECT id, username, password_hash, is_admin, enabled, must_change_password, created_at, updated_at
			FROM users WHERE username = ?
		`, username)
		return row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.Enabled, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUserByID retrieves a user by ID.
func (r *UserRepo) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	var u models.User
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		row := database.QueryRowContext(ctx, `
			SELECT id, username, password_hash, is_admin, enabled, must_change_password, created_at, updated_at
			FROM users WHERE id = ?
		`, id)
		return row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.Enabled, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdatePassword updates user password and clears must_change_password flag.
func (r *UserRepo) UpdatePassword(ctx context.Context, userID, newPassword string) error {
	pwdHash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := models.UTCNow()
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE users
			SET password_hash = ?, must_change_password = 0, updated_at = ?
			WHERE id = ?
		`, pwdHash, now, userID)
		return err
	})
}

// CreateSession stores an authenticated session token hash.
func (r *UserRepo) CreateSession(ctx context.Context, session *models.AuthSession) error {
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO auth_sessions (id, user_id, token_hash, audience, device_id, device_name, client_name, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, session.ID, session.UserID, session.TokenHash, session.Audience, session.DeviceID, session.DeviceName, session.ClientName, session.CreatedAt, session.ExpiresAt)
		return err
	})
}

// GetSessionByTokenHash retrieves an unexpired session matching token hash and audience.
func (r *UserRepo) GetSessionByTokenHash(ctx context.Context, tokenHash, audience, now string) (*models.AuthSession, *models.User, error) {
	var s models.AuthSession
	var u models.User

	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		row := database.QueryRowContext(ctx, `
			SELECT s.id, s.user_id, s.token_hash, s.audience, s.device_id, s.device_name, s.client_name, s.created_at, s.expires_at,
			       u.id, u.username, u.password_hash, u.is_admin, u.enabled, u.must_change_password, u.created_at, u.updated_at
			FROM auth_sessions s
			JOIN users u ON s.user_id = u.id
			WHERE s.token_hash = ? AND s.audience = ? AND s.expires_at > ? AND u.enabled = 1
		`, tokenHash, audience, now)
		return row.Scan(
			&s.ID, &s.UserID, &s.TokenHash, &s.Audience, &s.DeviceID, &s.DeviceName, &s.ClientName, &s.CreatedAt, &s.ExpiresAt,
			&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.Enabled, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt,
		)
	})

	if err != nil {
		return nil, nil, err
	}
	return &s, &u, nil
}

// DeleteSession deletes a specific session.
func (r *UserRepo) DeleteSession(ctx context.Context, sessionID string) error {
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE id = ?`, sessionID)
		return err
	})
}

// RevokeAllUserSessions revokes all sessions for a user (e.g. upon password change or reset).
func (r *UserRepo) RevokeAllUserSessions(ctx context.Context, userID string) error {
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ?`, userID)
		return err
	})
}
