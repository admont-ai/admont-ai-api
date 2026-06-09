package users

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const timeFormat = time.RFC3339

// internalUserID is a scalar subquery resolving an internal user's id by email.
const internalUserID = `(SELECT id FROM users WHERE provider = 'internal' AND email = $1)`

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// userColumns is the shared projection for reading a user joined to its
// optional credentials row.
const userColumns = `
	u.id, u.provider, u.email, u.first_name, u.last_name, u.super_admin, u.roles, u.status,
	COALESCE(c.password_expired, FALSE), c.password_changed_at, COALESCE(c.totp_enabled, FALSE),
	u.created_at, u.updated_at`

// scanUser scans a row produced with userColumns into a UserEntry.
func scanUser(row pgx.Row) (*UserEntry, error) {
	var u UserEntry
	var pwChangedAt *time.Time
	var createdAt, updatedAt time.Time
	if err := row.Scan(&u.ID, &u.Provider, &u.Email, &u.FirstName, &u.LastName, &u.SuperAdmin,
		&u.Roles, &u.Status, &u.PasswordExpired, &pwChangedAt, &u.TOTPEnabled, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	u.Internal = u.Provider == "internal"
	u.Suspended = u.Status == "suspended"
	if pwChangedAt != nil {
		u.PasswordChangedAt = pwChangedAt.UTC().Format(timeFormat)
	}
	u.CreatedAt = createdAt.UTC().Format(timeFormat)
	u.UpdatedAt = updatedAt.UTC().Format(timeFormat)
	return &u, nil
}

// --- Users (unified) ---

// ListUsers returns all users (internal and external).
func (s *Store) ListUsers(ctx context.Context) ([]UserEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT`+userColumns+`
		FROM users u LEFT JOIN credentials c ON c.user_id = u.id
		ORDER BY u.provider, u.email`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	var users []UserEntry
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

func (s *Store) listUsersByProvider(ctx context.Context, internal bool) ([]UserEntry, error) {
	op := "="
	if !internal {
		op = "<>"
	}
	rows, err := s.pool.Query(ctx, `SELECT`+userColumns+`
		FROM users u LEFT JOIN credentials c ON c.user_id = u.id
		WHERE u.provider `+op+` 'internal'
		ORDER BY u.provider, u.email`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	var users []UserEntry
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// GetUser retrieves a user by provider and email, or nil if not found.
func (s *Store) GetUser(ctx context.Context, provider, email string) (*UserEntry, error) {
	row := s.pool.QueryRow(ctx, `SELECT`+userColumns+`
		FROM users u LEFT JOIN credentials c ON c.user_id = u.id
		WHERE u.provider = $1 AND u.email = $2`, provider, email)
	u, err := scanUser(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting user %s:%s: %w", provider, email, err)
	}
	return u, nil
}

// GetUserID returns the id of a user by provider and email.
func (s *Store) GetUserID(ctx context.Context, provider, email string) (int, error) {
	var id int
	err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE provider = $1 AND email = $2`, provider, email).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("user %s:%s not found", provider, email)
	}
	if err != nil {
		return 0, fmt.Errorf("looking up user %s:%s: %w", provider, email, err)
	}
	return id, nil
}

// UpsertUser inserts or updates a user's profile fields (not credentials).
// The provider must be set on the entry ("internal" or an external IdP name).
func (s *Store) UpsertUser(ctx context.Context, u UserEntry) error {
	provider := u.Provider
	if provider == "" && u.Internal {
		provider = "internal"
	}
	status := u.Status
	if status == "" {
		status = "active"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (provider, email, first_name, last_name, super_admin, roles, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (provider, email) DO UPDATE SET
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			super_admin = EXCLUDED.super_admin,
			roles = EXCLUDED.roles,
			status = EXCLUDED.status
	`, provider, u.Email, u.FirstName, u.LastName, u.SuperAdmin, u.Roles, status)
	if err != nil {
		return fmt.Errorf("upserting user %s:%s: %w", provider, u.Email, err)
	}
	return nil
}

// CreateExternalUser inserts a new external user with the given status,
// doing nothing if one already exists. Used by the social-login flow.
func (s *Store) CreateExternalUser(ctx context.Context, provider, email, firstName, lastName, status string) error {
	if status == "" {
		status = "active"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (provider, email, first_name, last_name, status)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (provider, email) DO NOTHING
	`, provider, email, firstName, lastName, status)
	if err != nil {
		return fmt.Errorf("creating external user %s:%s: %w", provider, email, err)
	}
	return nil
}

// UpdateUserProfile refreshes only the display-name fields (e.g. from the IdP
// on each successful social login); it does not touch roles/status/suspended.
func (s *Store) UpdateUserProfile(ctx context.Context, provider, email, firstName, lastName string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET first_name = $3, last_name = $4 WHERE provider = $1 AND email = $2`,
		provider, email, firstName, lastName)
	if err != nil {
		return fmt.Errorf("updating profile for %s:%s: %w", provider, email, err)
	}
	return nil
}

// SetUserStatus updates a user's lifecycle status (e.g. approving a pending user).
func (s *Store) SetUserStatus(ctx context.Context, provider, email, status string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET status = $3 WHERE provider = $1 AND email = $2`,
		provider, email, status)
	if err != nil {
		return fmt.Errorf("setting status for %s:%s: %w", provider, email, err)
	}
	return nil
}

// DeleteUser removes a user by provider and email. Credentials and group
// memberships are removed via ON DELETE CASCADE.
func (s *Store) DeleteUser(ctx context.Context, provider, email string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM users WHERE provider = $1 AND email = $2`, provider, email)
	if err != nil {
		return fmt.Errorf("deleting user %s:%s: %w", provider, email, err)
	}
	return nil
}

// --- Internal users (compat wrappers) ---

// ListInternalUsers returns all internal (password) users.
func (s *Store) ListInternalUsers(ctx context.Context) ([]UserEntry, error) {
	return s.listUsersByProvider(ctx, true)
}

// GetInternalUser retrieves an internal user by email.
func (s *Store) GetInternalUser(ctx context.Context, email string) (*UserEntry, error) {
	return s.GetUser(ctx, "internal", email)
}

// UpsertInternalUser inserts or updates an internal user and ensures a
// credentials row exists (carrying password_expired).
func (s *Store) UpsertInternalUser(ctx context.Context, u UserEntry) error {
	u.Provider = "internal"
	if err := s.UpsertUser(ctx, u); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO credentials (user_id, password_expired)
		VALUES (`+internalUserID+`, $2)
		ON CONFLICT (user_id) DO UPDATE SET password_expired = EXCLUDED.password_expired
	`, u.Email, u.PasswordExpired)
	if err != nil {
		return fmt.Errorf("upserting credentials for %q: %w", u.Email, err)
	}
	return nil
}

// DeleteInternalUser removes an internal user by email.
func (s *Store) DeleteInternalUser(ctx context.Context, email string) error {
	return s.DeleteUser(ctx, "internal", email)
}

// GetInternalUserID returns the id of an internal user by email.
func (s *Store) GetInternalUserID(ctx context.Context, email string) (int, error) {
	return s.GetUserID(ctx, "internal", email)
}

// SetPasswordHash sets the password hash for an internal user, creating the
// credentials row if needed.
func (s *Store) SetPasswordHash(ctx context.Context, email, hash string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO credentials (user_id, password_hash, password_changed_at)
		VALUES (`+internalUserID+`, $2, NOW())
		ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash, password_changed_at = NOW()
	`, email, hash)
	if err != nil {
		return fmt.Errorf("setting password hash for %q: %w", email, err)
	}
	return nil
}

// GetPasswordHash returns the password hash for an internal user ("" if none).
func (s *Store) GetPasswordHash(ctx context.Context, email string) (string, error) {
	var hash string
	err := s.pool.QueryRow(ctx, `
		SELECT c.password_hash FROM credentials c
		JOIN users u ON u.id = c.user_id
		WHERE u.provider = 'internal' AND u.email = $1`, email).Scan(&hash)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting password hash for %q: %w", email, err)
	}
	return hash, nil
}

// ClearPasswordExpired sets password_expired to false for an internal user.
func (s *Store) ClearPasswordExpired(ctx context.Context, email string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE credentials SET password_expired = FALSE WHERE user_id = `+internalUserID, email)
	if err != nil {
		return fmt.Errorf("clearing password_expired for %q: %w", email, err)
	}
	return nil
}

// --- TOTP (internal users) ---

// SetTOTPSecret stores the encrypted TOTP secret (does not enable TOTP).
func (s *Store) SetTOTPSecret(ctx context.Context, email, encryptedSecret string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO credentials (user_id, totp_secret, totp_enabled)
		VALUES (`+internalUserID+`, $2, FALSE)
		ON CONFLICT (user_id) DO UPDATE SET totp_secret = EXCLUDED.totp_secret, totp_enabled = FALSE
	`, email, encryptedSecret)
	if err != nil {
		return fmt.Errorf("setting TOTP secret for %q: %w", email, err)
	}
	return nil
}

// EnableTOTP sets totp_enabled to true for an internal user.
func (s *Store) EnableTOTP(ctx context.Context, email string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE credentials SET totp_enabled = TRUE WHERE user_id = `+internalUserID, email)
	if err != nil {
		return fmt.Errorf("enabling TOTP for %q: %w", email, err)
	}
	return nil
}

// DisableTOTP clears the TOTP secret, disables TOTP, and removes recovery codes.
func (s *Store) DisableTOTP(ctx context.Context, email string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE credentials SET totp_secret = '', totp_enabled = FALSE, totp_recovery_codes = '{}' WHERE user_id = `+internalUserID, email)
	if err != nil {
		return fmt.Errorf("disabling TOTP for %q: %w", email, err)
	}
	return nil
}

// GetTOTPSecret returns the encrypted TOTP secret and enabled status.
func (s *Store) GetTOTPSecret(ctx context.Context, email string) (string, bool, error) {
	var secret string
	var enabled bool
	err := s.pool.QueryRow(ctx, `
		SELECT c.totp_secret, c.totp_enabled FROM credentials c
		JOIN users u ON u.id = c.user_id
		WHERE u.provider = 'internal' AND u.email = $1`, email).Scan(&secret, &enabled)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("getting TOTP secret for %q: %w", email, err)
	}
	return secret, enabled, nil
}

// IsTOTPEnabled returns whether TOTP is enabled for an internal user.
func (s *Store) IsTOTPEnabled(ctx context.Context, email string) (bool, error) {
	var enabled bool
	err := s.pool.QueryRow(ctx, `
		SELECT c.totp_enabled FROM credentials c
		JOIN users u ON u.id = c.user_id
		WHERE u.provider = 'internal' AND u.email = $1`, email).Scan(&enabled)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking TOTP status for %q: %w", email, err)
	}
	return enabled, nil
}

// SetTOTPRecoveryCodes stores bcrypt-hashed recovery codes for an internal user.
func (s *Store) SetTOTPRecoveryCodes(ctx context.Context, email string, hashedCodes []string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO credentials (user_id, totp_recovery_codes)
		VALUES (`+internalUserID+`, $2)
		ON CONFLICT (user_id) DO UPDATE SET totp_recovery_codes = EXCLUDED.totp_recovery_codes
	`, email, hashedCodes)
	if err != nil {
		return fmt.Errorf("setting TOTP recovery codes for %q: %w", email, err)
	}
	return nil
}

// GetTOTPRecoveryCodes returns the bcrypt-hashed recovery codes for an internal user.
func (s *Store) GetTOTPRecoveryCodes(ctx context.Context, email string) ([]string, error) {
	var codes []string
	err := s.pool.QueryRow(ctx, `
		SELECT c.totp_recovery_codes FROM credentials c
		JOIN users u ON u.id = c.user_id
		WHERE u.provider = 'internal' AND u.email = $1`, email).Scan(&codes)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting TOTP recovery codes for %q: %w", email, err)
	}
	return codes, nil
}

// UpdateTOTPRecoveryCodes replaces the recovery codes (e.g. after consuming one).
func (s *Store) UpdateTOTPRecoveryCodes(ctx context.Context, email string, remainingCodes []string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE credentials SET totp_recovery_codes = $2 WHERE user_id = `+internalUserID, email, remainingCodes)
	if err != nil {
		return fmt.Errorf("updating TOTP recovery codes for %q: %w", email, err)
	}
	return nil
}

// --- External users (compat wrappers) ---

// ListExternalUsers returns all external (IdP) users.
func (s *Store) ListExternalUsers(ctx context.Context) ([]UserEntry, error) {
	return s.listUsersByProvider(ctx, false)
}

// GetExternalUser retrieves an external user by provider name and email.
func (s *Store) GetExternalUser(ctx context.Context, provider, email string) (*UserEntry, error) {
	return s.GetUser(ctx, provider, email)
}

// UpsertExternalUser inserts or updates an external user (provider name on entry).
func (s *Store) UpsertExternalUser(ctx context.Context, u UserEntry) error {
	return s.UpsertUser(ctx, u)
}

// DeleteExternalUser removes an external user by provider name and email.
func (s *Store) DeleteExternalUser(ctx context.Context, provider, email string) error {
	return s.DeleteUser(ctx, provider, email)
}

// GetExternalUserID returns the id of an external user by provider name and email.
func (s *Store) GetExternalUserID(ctx context.Context, provider, email string) (int, error) {
	return s.GetUserID(ctx, provider, email)
}

// --- Combined ---

// ListAllUsers returns all users.
func (s *Store) ListAllUsers(ctx context.Context) ([]UserEntry, error) {
	return s.ListUsers(ctx)
}

// --- Groups ---

// ListGroups returns all user groups with their members.
func (s *Store) ListGroups(ctx context.Context) ([]UserGroup, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, roles FROM user_groups ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing groups: %w", err)
	}
	defer rows.Close()

	var groups []UserGroup
	var groupIDs []int
	for rows.Next() {
		var id int
		var g UserGroup
		if err := rows.Scan(&id, &g.Name, &g.Description, &g.Roles); err != nil {
			return nil, err
		}
		groups = append(groups, g)
		groupIDs = append(groupIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, gid := range groupIDs {
		members, err := s.getGroupMembers(ctx, gid)
		if err != nil {
			return nil, err
		}
		groups[i].Members = members
	}
	return groups, nil
}

// GetGroup retrieves a single group by name, including its members.
func (s *Store) GetGroup(ctx context.Context, name string) (*UserGroup, error) {
	var id int
	var g UserGroup
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, roles FROM user_groups WHERE name = $1`, name,
	).Scan(&id, &g.Name, &g.Description, &g.Roles)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting group %q: %w", name, err)
	}

	members, err := s.getGroupMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	g.Members = members
	return &g, nil
}

// UpsertGroup inserts or updates a user group (name, description, roles only).
func (s *Store) UpsertGroup(ctx context.Context, g UserGroup) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_groups (name, description, roles) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET description = EXCLUDED.description,
		                                  roles = EXCLUDED.roles
	`, g.Name, g.Description, g.Roles)
	if err != nil {
		return fmt.Errorf("upserting group %q: %w", g.Name, err)
	}
	return nil
}

// DeleteGroup removes a group by name. Members are cascade-deleted via FK.
func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM user_groups WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("deleting group %q: %w", name, err)
	}
	return nil
}

// SetGroupMembers replaces all members of a group with the given user ids.
func (s *Store) SetGroupMembers(ctx context.Context, groupName string, members []GroupMemberRef) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var groupID int
	if err := tx.QueryRow(ctx, `SELECT id FROM user_groups WHERE name = $1`, groupName).Scan(&groupID); err != nil {
		return fmt.Errorf("group %q not found: %w", groupName, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM user_group_members WHERE group_id = $1`, groupID); err != nil {
		return fmt.Errorf("clearing members of group %q: %w", groupName, err)
	}

	for _, m := range members {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_group_members (group_id, user_id) VALUES ($1, $2)`,
			groupID, m.UserID); err != nil {
			return fmt.Errorf("adding member (user_id=%d) to group %q: %w", m.UserID, groupName, err)
		}
	}

	return tx.Commit(ctx)
}

// getGroupMembers returns the users belonging to a group.
func (s *Store) getGroupMembers(ctx context.Context, groupID int) ([]UserEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT`+userColumns+`
		FROM user_group_members ugm
		JOIN users u ON u.id = ugm.user_id
		LEFT JOIN credentials c ON c.user_id = u.id
		WHERE ugm.group_id = $1
		ORDER BY u.provider, u.email`, groupID)
	if err != nil {
		return nil, fmt.Errorf("listing group members: %w", err)
	}
	defer rows.Close()

	var members []UserEntry
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, *u)
	}
	return members, rows.Err()
}
