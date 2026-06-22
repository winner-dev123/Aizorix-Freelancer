-- Example sqlc queries for the users table (see db/migrations/000002_auth_and_rbac.up.sql).
-- Generate Go with: make sqlc   (output in db/generated).

-- name: GetUserByID :one
SELECT id, email, phone, password_hash, status, primary_type,
       email_verified, phone_verified, mfa_enabled, residency_country,
       locale, last_login_at, created_at, updated_at
FROM users
WHERE id = $1
  AND deleted_at IS NULL;

-- name: GetUserByEmail :one
SELECT id, email, password_hash, status, primary_type,
       email_verified, mfa_enabled, created_at
FROM users
WHERE email = $1
  AND deleted_at IS NULL;

-- name: ListActiveUsers :many
SELECT id, email, primary_type, status, created_at
FROM users
WHERE deleted_at IS NULL
  AND status = 'active'
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: CreateUser :one
INSERT INTO users (email, password_hash, status, primary_type)
VALUES ($1, $2, $3, $4)
RETURNING id, email, status, primary_type, created_at;

-- name: MarkEmailVerified :exec
UPDATE users
SET email_verified = true
WHERE id = $1
  AND deleted_at IS NULL;
