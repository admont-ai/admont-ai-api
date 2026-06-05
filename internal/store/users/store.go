package users

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const timeFormat = time.RFC3339

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// --- Internal users ---

// ListInternalUsers returns all internal users.
func (s *Store) ListInternalUsers(ctx context.Context) ([]UserEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, email, first_name, last_name, super_admin, roles, password_expired, suspended, password_changed_at, totp_enabled, created_at, updated_at
		FROM internal_users
		ORDER BY email
	`)
	if err != nil {
		return nil, fmt.Errorf("listing internal users: %w", err)
	}
	defer rows.Close()

	var users []UserEntry
	for rows.Next() {
		var u UserEntry
		var pwChangedAt *time.Time
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName, &u.SuperAdmin, &u.Roles, &u.PasswordExpired, &u.Suspended, &pwChangedAt, &u.TOTPEnabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		u.Internal = true
		if pwChangedAt != nil {
			u.PasswordChangedAt = pwChangedAt.UTC().Format(timeFormat)
		}
		u.CreatedAt = createdAt.UTC().Format(timeFormat)
		u.UpdatedAt = updatedAt.UTC().Format(timeFormat)
		users = append(users, u)
	}
	return users, rows.Err()
}

// GetInternalUser retrieves an internal user by email.
func (s *Store) GetInternalUser(ctx context.Context, email string) (*UserEntry, error) {
	var u UserEntry
	var pwChangedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, first_name, last_name, super_admin, roles, password_expired, suspended, password_changed_at, totp_enabled
		FROM internal_users
		WHERE email = $1
	`, email).Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName, &u.SuperAdmin, &u.Roles, &u.PasswordExpired, &u.Suspended, &pwChangedAt, &u.TOTPEnabled)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting internal user %q: %w", email, err)
	}
	u.Internal = true
	if pwChangedAt != nil {
		u.PasswordChangedAt = pwChangedAt.UTC().Format(timeFormat)
	}
	return &u, nil
}

// UpsertInternalUser inserts or updates an internal user.
func (s *Store) UpsertInternalUser(ctx context.Context, u UserEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO internal_users (email, first_name, last_name, super_admin, roles, password_expired, suspended)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (email) DO UPDATE SET
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			super_admin = EXCLUDED.super_admin,
			roles = EXCLUDED.roles,
			password_expired = EXCLUDED.password_expired,
			suspended = EXCLUDED.suspended
	`, u.Email, u.FirstName, u.LastName, u.SuperAdmin, u.Roles, u.PasswordExpired, u.Suspended)
	if err != nil {
		return fmt.Errorf("upserting internal user %q: %w", u.Email, err)
	}
	return nil
}

// DeleteInternalUser removes an internal user by email.
func (s *Store) DeleteInternalUser(ctx context.Context, email string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM internal_users WHERE email = $1`, email)
	if err != nil {
		return fmt.Errorf("deleting internal user %q: %w", email, err)
	}
	return nil
}

// SetPasswordHash sets the password hash for an internal user.
func (s *Store) SetPasswordHash(ctx context.Context, email, hash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE internal_users SET password_hash = $2, password_changed_at = NOW() WHERE email = $1`,
		email, hash,
	)
	if err != nil {
		return fmt.Errorf("setting password hash for %q: %w", email, err)
	}
	return nil
}

// GetPasswordHash returns the password hash for an internal user.
func (s *Store) GetPasswordHash(ctx context.Context, email string) (string, error) {
	var hash string
	err := s.pool.QueryRow(ctx,
		`SELECT password_hash FROM internal_users WHERE email = $1`,
		email,
	).Scan(&hash)
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
		`UPDATE internal_users SET password_expired = FALSE WHERE email = $1`,
		email,
	)
	if err != nil {
		return fmt.Errorf("clearing password_expired for %q: %w", email, err)
	}
	return nil
}

// GetInternalUserID returns the internal user ID for a given email.
func (s *Store) GetInternalUserID(ctx context.Context, email string) (int, error) {
	var id int
	err := s.pool.QueryRow(ctx, `SELECT id FROM internal_users WHERE email = $1`, email).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("internal user %q not found", email)
	}
	if err != nil {
		return 0, fmt.Errorf("looking up internal user %q: %w", email, err)
	}
	return id, nil
}

// --- TOTP ---

// SetTOTPSecret stores the encrypted TOTP secret for an internal user (does not enable TOTP).
func (s *Store) SetTOTPSecret(ctx context.Context, email, encryptedSecret string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE internal_users SET totp_secret = $2, totp_enabled = FALSE WHERE email = $1`,
		email, encryptedSecret,
	)
	if err != nil {
		return fmt.Errorf("setting TOTP secret for %q: %w", email, err)
	}
	return nil
}

// EnableTOTP sets totp_enabled to true for an internal user.
func (s *Store) EnableTOTP(ctx context.Context, email string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE internal_users SET totp_enabled = TRUE WHERE email = $1`,
		email,
	)
	if err != nil {
		return fmt.Errorf("enabling TOTP for %q: %w", email, err)
	}
	return nil
}

// DisableTOTP clears the TOTP secret, disables TOTP, and removes recovery codes.
func (s *Store) DisableTOTP(ctx context.Context, email string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE internal_users SET totp_secret = '', totp_enabled = FALSE, totp_recovery_codes = '{}' WHERE email = $1`,
		email,
	)
	if err != nil {
		return fmt.Errorf("disabling TOTP for %q: %w", email, err)
	}
	return nil
}

// GetTOTPSecret returns the encrypted TOTP secret and enabled status for an internal user.
func (s *Store) GetTOTPSecret(ctx context.Context, email string) (string, bool, error) {
	var secret string
	var enabled bool
	err := s.pool.QueryRow(ctx,
		`SELECT totp_secret, totp_enabled FROM internal_users WHERE email = $1`,
		email,
	).Scan(&secret, &enabled)
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
	err := s.pool.QueryRow(ctx,
		`SELECT totp_enabled FROM internal_users WHERE email = $1`,
		email,
	).Scan(&enabled)
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
	_, err := s.pool.Exec(ctx,
		`UPDATE internal_users SET totp_recovery_codes = $2 WHERE email = $1`,
		email, hashedCodes,
	)
	if err != nil {
		return fmt.Errorf("setting TOTP recovery codes for %q: %w", email, err)
	}
	return nil
}

// GetTOTPRecoveryCodes returns the bcrypt-hashed recovery codes for an internal user.
func (s *Store) GetTOTPRecoveryCodes(ctx context.Context, email string) ([]string, error) {
	var codes []string
	err := s.pool.QueryRow(ctx,
		`SELECT totp_recovery_codes FROM internal_users WHERE email = $1`,
		email,
	).Scan(&codes)
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
		`UPDATE internal_users SET totp_recovery_codes = $2 WHERE email = $1`,
		email, remainingCodes,
	)
	if err != nil {
		return fmt.Errorf("updating TOTP recovery codes for %q: %w", email, err)
	}
	return nil
}

// --- External users ---

// ListExternalUsers returns all external users.
func (s *Store) ListExternalUsers(ctx context.Context) ([]UserEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.provider_id, ap.name, u.email, u.first_name, u.last_name, u.super_admin, u.roles, u.created_at, u.updated_at
		FROM external_users u
		JOIN auth_providers ap ON ap.id = u.provider_id
		ORDER BY ap.name, u.email
	`)
	if err != nil {
		return nil, fmt.Errorf("listing external users: %w", err)
	}
	defer rows.Close()

	var users []UserEntry
	for rows.Next() {
		var u UserEntry
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&u.ID, &u.ProviderID, &u.Provider, &u.Email, &u.FirstName, &u.LastName, &u.SuperAdmin, &u.Roles, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		u.CreatedAt = createdAt.UTC().Format(timeFormat)
		u.UpdatedAt = updatedAt.UTC().Format(timeFormat)
		users = append(users, u)
	}
	return users, rows.Err()
}

// GetExternalUser retrieves an external user by provider ID and email.
func (s *Store) GetExternalUser(ctx context.Context, providerID int, email string) (*UserEntry, error) {
	var u UserEntry
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.provider_id, ap.name, u.email, u.first_name, u.last_name, u.super_admin, u.roles
		FROM external_users u
		JOIN auth_providers ap ON ap.id = u.provider_id
		WHERE u.provider_id = $1 AND u.email = $2
	`, providerID, email).Scan(&u.ID, &u.ProviderID, &u.Provider, &u.Email, &u.FirstName, &u.LastName, &u.SuperAdmin, &u.Roles)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting external user (provider_id=%d, email=%s): %w", providerID, email, err)
	}
	return &u, nil
}

// UpsertExternalUser inserts or updates an external user.
func (s *Store) UpsertExternalUser(ctx context.Context, u UserEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO external_users (provider_id, email, first_name, last_name, super_admin, roles)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (provider_id, email) DO UPDATE SET
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			super_admin = EXCLUDED.super_admin,
			roles = EXCLUDED.roles
	`, u.ProviderID, u.Email, u.FirstName, u.LastName, u.SuperAdmin, u.Roles)
	if err != nil {
		return fmt.Errorf("upserting external user (provider_id=%d, email=%s): %w", u.ProviderID, u.Email, err)
	}
	return nil
}

// DeleteExternalUser removes an external user by provider ID and email.
func (s *Store) DeleteExternalUser(ctx context.Context, providerID int, email string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM external_users WHERE provider_id = $1 AND email = $2`,
		providerID, email,
	)
	if err != nil {
		return fmt.Errorf("deleting external user (provider_id=%d, email=%s): %w", providerID, email, err)
	}
	return nil
}

// GetExternalUserID returns the external user ID for a given provider name and email.
func (s *Store) GetExternalUserID(ctx context.Context, providerName, email string) (int, error) {
	var id int
	err := s.pool.QueryRow(ctx, `
		SELECT u.id FROM external_users u
		JOIN auth_providers ap ON ap.id = u.provider_id
		WHERE ap.name = $1 AND u.email = $2
	`, providerName, email).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("external user %s:%s not found", providerName, email)
	}
	if err != nil {
		return 0, fmt.Errorf("looking up external user %s:%s: %w", providerName, email, err)
	}
	return id, nil
}

// --- Combined ---

// ListAllUsers returns all internal and external users combined.
func (s *Store) ListAllUsers(ctx context.Context) ([]UserEntry, error) {
	internal, err := s.ListInternalUsers(ctx)
	if err != nil {
		return nil, err
	}
	external, err := s.ListExternalUsers(ctx)
	if err != nil {
		return nil, err
	}
	return append(internal, external...), nil
}

// --- Groups ---

// ListGroups returns all user groups with their members from the database.
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

// SetGroupMembers replaces all members of a group with the given member refs.
func (s *Store) SetGroupMembers(ctx context.Context, groupName string, members []GroupMemberRef) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Get group ID
	var groupID int
	err = tx.QueryRow(ctx, `SELECT id FROM user_groups WHERE name = $1`, groupName).Scan(&groupID)
	if err != nil {
		return fmt.Errorf("group %q not found: %w", groupName, err)
	}

	// Clear existing members
	_, err = tx.Exec(ctx, `DELETE FROM user_group_members WHERE group_id = $1`, groupID)
	if err != nil {
		return fmt.Errorf("clearing members of group %q: %w", groupName, err)
	}

	// Insert new members
	for _, m := range members {
		_, err = tx.Exec(ctx,
			`INSERT INTO user_group_members (group_id, user_id, internal_user) VALUES ($1, $2, $3)`,
			groupID, m.UserID, m.InternalUser)
		if err != nil {
			return fmt.Errorf("adding member (user_id=%d, internal=%v) to group %q: %w", m.UserID, m.InternalUser, groupName, err)
		}
	}

	return tx.Commit(ctx)
}

// getGroupMembers returns the users belonging to a group, from both internal and external tables.
func (s *Store) getGroupMembers(ctx context.Context, groupID int) ([]UserEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ugm.user_id, ugm.internal_user
		FROM user_group_members ugm
		WHERE ugm.group_id = $1
		ORDER BY ugm.internal_user DESC, ugm.user_id
	`, groupID)
	if err != nil {
		return nil, fmt.Errorf("listing group members: %w", err)
	}
	defer rows.Close()

	var members []UserEntry
	for rows.Next() {
		var userID int
		var internal bool
		if err := rows.Scan(&userID, &internal); err != nil {
			return nil, err
		}

		if internal {
			u, err := s.getInternalUserByID(ctx, userID)
			if err != nil {
				return nil, err
			}
			if u != nil {
				members = append(members, *u)
			}
		} else {
			u, err := s.getExternalUserByID(ctx, userID)
			if err != nil {
				return nil, err
			}
			if u != nil {
				members = append(members, *u)
			}
		}
	}
	return members, rows.Err()
}

func (s *Store) getInternalUserByID(ctx context.Context, id int) (*UserEntry, error) {
	var u UserEntry
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, first_name, last_name, super_admin, roles
		FROM internal_users WHERE id = $1
	`, id).Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName, &u.SuperAdmin, &u.Roles)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting internal user by id %d: %w", id, err)
	}
	u.Internal = true
	return &u, nil
}

func (s *Store) getExternalUserByID(ctx context.Context, id int) (*UserEntry, error) {
	var u UserEntry
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.provider_id, ap.name, u.email, u.first_name, u.last_name, u.super_admin, u.roles
		FROM external_users u
		JOIN auth_providers ap ON ap.id = u.provider_id
		WHERE u.id = $1
	`, id).Scan(&u.ID, &u.ProviderID, &u.Provider, &u.Email, &u.FirstName, &u.LastName, &u.SuperAdmin, &u.Roles)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting external user by id %d: %w", id, err)
	}
	return &u, nil
}
