package portal

import (
	"context"
	"fmt"

	"github.com/Wysted/atrium/platform/api/internal/adminz"
)

// MaxUserMatches bounds the users returned for one email (the email is not unique).
const MaxUserMatches = 50

type userStatusDoc struct {
	Email           string   `json:"email"`
	Name            string   `json:"name,omitempty"` // names a robot, which has no email
	Status          string   `json:"status"`
	SessionsRevoked *int64   `json:"sessions_revoked,omitempty"`
	TokensRevoked   []string `json:"tokens_revoked,omitempty"` // ids, so each one can be traced
}

type sessionsDoc struct {
	Email   string `json:"email"`
	Name    string `json:"name,omitempty"` // names a robot, which has no email
	Revoked int64  `json:"revoked"`
}

// nameIfNoEmail is the name a document carries for a subject without an email (a robot); humans are identified
// by their email, as before.
func nameIfNoEmail(email, name string) string {
	if email != "" {
		return ""
	}
	return name
}

// FindUsers returns the users with that email, compared case-insensitively (platform admins only). The email
// is not unique, so there can be several; at most MaxUserMatches are returned.
func (s *Service) FindUsers(ctx context.Context, callerID, email string) ([]User, error) {
	var out []User
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = nil
		_, d, err := begin(ctx, tx, callerID, adminz.UserList, adminz.Resource{Type: TargetUser})
		if err != nil {
			return err
		}
		email := normalizeEmail(email)
		switch {
		case email == "":
			return invalid("email", CodeRequired)
		case !validEmail(email):
			return invalid("email", CodeInvalid)
		}
		out, err = tx.Users().ListUsersByEmail(ctx, d.Scope, email, MaxUserMatches)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		return nil
	})
	return out, err
}

// authorizeUserTarget authorizes an area-scoped action on a human (the target of GET /users/{id}/...). The area is
// resolved from storage unless the caller is a platform admin, who passes whatever it says. A robot or unknown id is
// ErrNotFound before anything else, like the rest of /users/*.
func authorizeUserTarget(ctx context.Context, tx Tx, callerID, userID string, action adminz.Action) error {
	sub, err := subject(ctx, tx, callerID)
	if err != nil {
		return err
	}
	u, err := tx.Users().GetUser(ctx, userID) // humans only
	if err != nil {
		return err
	}
	res := adminz.Resource{Type: TargetUser, ID: u.ID}
	if !sub.PlatformAdmin {
		if res.AreaID, err = personArea(ctx, tx, u.ID); err != nil {
			return err
		}
	}
	_, err = authorize(sub, action, res)
	return err
}

// SetUserStatus disables or enables a user (platform admins only). Disabling also deletes the user's sessions
// and revokes their API tokens in the same transaction, so the cutoff does not wait for the sessions to expire. An administrator cannot
// disable themselves (ErrSelfDisable): it would lock them out of the portal. Setting the status a user already
// has changes nothing, writes no audit row and returns the user as is.
func (s *Service) SetUserStatus(ctx context.Context, callerID, userID, status string) (User, error) {
	userID, ok := canonicalUUID(userID)
	if !ok {
		return User{}, ErrNotFound
	}
	action := adminz.UserDisable
	if status == UserStatusActive {
		action = adminz.UserEnable
	}
	var out User
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = User{}
		if _, _, err := begin(ctx, tx, callerID, action, adminz.Resource{Type: TargetUser, ID: userID}); err != nil {
			return err
		}
		if status != UserStatusActive && status != UserStatusDisabled {
			if status == "" {
				return invalid("status", CodeRequired)
			}
			return invalid("status", CodeInvalid)
		}
		var admins []string
		if status == UserStatusDisabled {
			// Lock order of the users table: the active platform admins by id, then the target. Two
			// administrators disabling each other thus run one after the other, and the second one finds
			// its own account already disabled when the caller's permissions are read again below.
			// Nothing here is held together with the initiative and grant locks of grants.go, and those
			// code paths take no user lock, so the two orders cannot form a cycle.
			var err error
			if admins, err = tx.Users().LockActiveAdmins(ctx); err != nil {
				return fmt.Errorf("lock platform admins: %w", err)
			}
			if _, _, err := begin(ctx, tx, callerID, action, adminz.Resource{Type: TargetUser, ID: userID}); err != nil {
				return err
			}
		}
		u, err := tx.Users().LockUser(ctx, userID)
		if err != nil {
			return err
		}
		if u.ID == callerID && status == UserStatusDisabled {
			return ErrSelfDisable
		}
		if status == UserStatusDisabled && u.Status == UserStatusActive && isOnlyAdmin(admins, u.ID) {
			return ErrLastAdmin
		}
		if u.Status == status {
			out = u
			return nil
		}
		if err := tx.Users().SetStatus(ctx, u.ID, status); err != nil {
			return fmt.Errorf("set user status: %w", err)
		}
		after := userStatusDoc{Email: u.Email, Name: nameIfNoEmail(u.Email, u.Name), Status: status}
		if status == UserStatusDisabled {
			n, err := tx.Users().DeleteSessions(ctx, u.ID)
			if err != nil {
				return fmt.Errorf("delete user sessions: %w", err)
			}
			after.SessionsRevoked = &n
			if after.TokensRevoked, err = tx.Users().RevokeTokens(ctx, u.ID); err != nil {
				return fmt.Errorf("revoke user tokens: %w", err)
			}
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: action, targetType: TargetUser, targetID: u.ID,
			before: userStatusDoc{Email: u.Email, Name: nameIfNoEmail(u.Email, u.Name), Status: u.Status}, after: after,
		}); err != nil {
			return err
		}
		u.Status = status
		out = u
		return nil
	})
	return out, err
}

// RevokeUserSessions deletes every session of a user (platform admins only) and returns how many central
// sessions were removed. It is audited even when there were none: it is an administrative action.
func (s *Service) RevokeUserSessions(ctx context.Context, callerID, userID string) (int64, error) {
	userID, ok := canonicalUUID(userID)
	if !ok {
		return 0, ErrNotFound
	}
	var revoked int64
	err := s.store.WithTx(ctx, func(tx Tx) error {
		revoked = 0
		if _, _, err := begin(ctx, tx, callerID, adminz.UserSessionsRevoke, adminz.Resource{Type: TargetUser, ID: userID}); err != nil {
			return err
		}
		u, err := tx.Users().GetUser(ctx, userID)
		if err != nil {
			return err
		}
		n, err := tx.Users().DeleteSessions(ctx, u.ID)
		if err != nil {
			return fmt.Errorf("delete user sessions: %w", err)
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.UserSessionsRevoke, targetType: TargetUserSessions, targetID: u.ID,
			after: sessionsDoc{Email: u.Email, Name: nameIfNoEmail(u.Email, u.Name), Revoked: n},
		}); err != nil {
			return err
		}
		revoked = n
		return nil
	})
	return revoked, err
}

// isOnlyAdmin reports whether id is an active platform admin and the only one.
func isOnlyAdmin(admins []string, id string) bool {
	return len(admins) == 1 && admins[0] == id
}
