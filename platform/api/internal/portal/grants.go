package portal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"platform/api/internal/adminz"
)

// grantDoc is the audit document of a grant (before for a revocation, after for a creation).
type grantDoc struct {
	InitiativeID string     `json:"initiative_id"`
	RoleID       string     `json:"role_id"`
	RoleKey      string     `json:"role_key"`
	UserID       string     `json:"user_id,omitempty"`
	UserEmail    string     `json:"user_email,omitempty"`
	GroupID      string     `json:"group_id,omitempty"`
	GroupKey     string     `json:"group_key,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

func docOfGrant(g Grant) grantDoc {
	d := grantDoc{InitiativeID: g.InitiativeID, RoleID: g.Role.ID, RoleKey: g.Role.Key, ExpiresAt: g.ExpiresAt}
	if g.User != nil {
		d.UserID, d.UserEmail = g.User.ID, g.User.Email
	}
	if g.Group != nil {
		d.GroupID, d.GroupKey = g.Group.ID, g.Group.Key
	}
	return d
}

// ListGrants lists the grants of an initiative, oldest first (keyset on the grant id, which is time-ordered).
// The scope comes from adminz.Decide: the repository cannot be asked for anything wider.
func (s *Service) ListGrants(ctx context.Context, callerID, initiativeID string, req PageRequest) (Page[Grant], error) {
	initiativeID, ok := canonicalUUID(initiativeID)
	if !ok {
		return Page[Grant]{}, ErrNotFound
	}
	size := pageSize(req.Limit)
	var out Page[Grant]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[Grant]{}
		_, d, err := begin(ctx, tx, callerID, adminz.GrantList, adminz.Resource{Type: TargetGrant, InitiativeID: initiativeID})
		if err != nil {
			return err
		}
		if err := mustExist(tx.Grants().InitiativeExists(ctx, initiativeID)); err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindGrants, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Grants().ListGrants(ctx, d.Scope, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list grants: %w", err)
		}
		out = pageOf(rows, size, cursorKindGrants, func(g Grant) Cursor { return Cursor{ID: g.ID} })
		return nil
	})
	return out, err
}

func lowerIfUUID(s string) string {
	if id, ok := canonicalUUID(s); ok {
		return id
	}
	return s
}

// mustExist turns "does not exist" into ErrNotFound.
func mustExist(exists bool, err error) error {
	if err != nil {
		return fmt.Errorf("check existence: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// CreateGrant grants a role of the initiative to a user (by email: exactly one active user) or to a group.
// A repeated request fails with ErrGrantExists and creates no second row (the natural key is initiative, role
// and subject). The role must belong to the initiative; the composite foreign key guarantees it and its
// violation is reported as a validation problem on role_id.
func (s *Service) CreateGrant(ctx context.Context, callerID, initiativeID string, in NewGrant) (Grant, error) {
	initiativeID, ok := canonicalUUID(initiativeID)
	if !ok {
		return Grant{}, ErrNotFound
	}
	in.RoleID, in.GroupID = lowerIfUUID(in.RoleID), lowerIfUUID(in.GroupID)
	var out Grant
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Grant{}
		_, _, err := begin(ctx, tx, callerID, adminz.GrantCreate, adminz.Resource{Type: TargetGrant, InitiativeID: initiativeID})
		if err != nil {
			return err
		}
		// Share lock on the initiative (the first lock, as in DeleteGrant): a revocation in progress finishes
		// first, and the caller's permissions are read again, so a manager revoked a moment ago cannot still
		// insert. Creations among themselves do not wait for each other.
		if err := tx.Grants().LockInitiativeShared(ctx, initiativeID); err != nil {
			return err
		}
		if _, _, err := begin(ctx, tx, callerID, adminz.GrantCreate, adminz.Resource{Type: TargetGrant, InitiativeID: initiativeID}); err != nil {
			return err
		}
		if err := s.validateNewGrant(in); err != nil {
			return err
		}
		row := GrantRow{InitiativeID: initiativeID, RoleID: in.RoleID, ExpiresAt: in.ExpiresAt, GrantedBy: callerID}
		if in.Email != "" {
			u, err := resolveActiveUser(ctx, tx, normalizeEmail(in.Email))
			if err != nil {
				return err
			}
			row.UserID = u.ID
		} else {
			if _, err := tx.Groups().GetGroup(ctx, in.GroupID); errors.Is(err, ErrNotFound) {
				return invalid("group_id", CodeUnknown)
			} else if err != nil {
				return fmt.Errorf("get group: %w", err)
			}
			row.GroupID = in.GroupID
		}

		id, err := tx.Grants().InsertGrant(ctx, row)
		if err != nil {
			return grantWriteError(err)
		}
		g, err := tx.Grants().GetGrant(ctx, id)
		if err != nil {
			return fmt.Errorf("read created grant: %w", err)
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.GrantCreate, targetType: TargetGrant, targetID: g.ID,
			initiativeID: initiativeID, after: docOfGrant(g),
		}); err != nil {
			return err
		}
		out = g
		return nil
	})
	return out, err
}

// validateNewGrant checks the shape of the request: no database involved.
func (s *Service) validateNewGrant(in NewGrant) error {
	var v ValidationError
	switch hasEmail, hasGroup := in.Email != "", in.GroupID != ""; {
	case hasEmail == hasGroup:
		v.Problems = append(v.Problems,
			FieldProblem{Field: "email", Code: CodeExclusive}, FieldProblem{Field: "group_id", Code: CodeExclusive})
	case hasGroup && !IsUUID(in.GroupID):
		v.Problems = append(v.Problems, FieldProblem{Field: "group_id", Code: CodeInvalid})
	}
	switch {
	case in.RoleID == "":
		v.Problems = append(v.Problems, FieldProblem{Field: "role_id", Code: CodeRequired})
	case !IsUUID(in.RoleID):
		v.Problems = append(v.Problems, FieldProblem{Field: "role_id", Code: CodeInvalid})
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.now()) {
		v.Problems = append(v.Problems, FieldProblem{Field: "expires_at", Code: CodeNotInTheFuture})
	}
	if len(v.Problems) > 0 {
		return &v
	}
	return nil
}

func grantWriteError(err error) error {
	var ref *ReferenceError
	switch {
	case errors.Is(err, ErrDuplicate):
		return ErrGrantExists
	case errors.As(err, &ref):
		return invalid(ref.Field, CodeUnknown)
	default:
		return fmt.Errorf("insert grant: %w", err)
	}
}

// DeleteGrant revokes a grant. A grant that does not exist, or belongs to an initiative the caller does not
// manage, is ErrNotFound; repeating a revocation therefore answers ErrNotFound.
//
// The last-manager rule: a grant of a role with manages_access cannot be revoked if it is the only effective
// one left in its initiative (ErrLastManager). A manages_access grant is effective when it is not expired and
// its subject has at least one active user: the grant's own user is active, or the group has an active member.
// This counts like the permissions loader (non-expired, direct or through a group) with one addition: a
// disabled user is no manager (Permissions.Subject refuses them), so a grant that only disabled users hold
// does not keep the initiative managed. Revoking a grant that is not effective is always allowed, because it
// removes no manager. The rule applies to platform admins too: they can grant first and revoke afterwards.
//
// Lock order of the portal, global: initiative row, then that initiative's grants by id (this file); the users
// table is locked separately (active platform admins by id, then the target; see SetUserStatus). No path takes
// explicit locks of both families; inserts referencing users take implicit KEY SHARE locks through their foreign
// keys, which never conflict with FOR NO KEY UPDATE. That compatibility rules out a cycle: never lock users rows
// FOR UPDATE.
//
// Concurrency: the transaction first locks the initiative row and then the initiative's manages_access
// grants (ordered by id), so two revocations of the same initiative run one after the other and the second
// counts what the first left. The caller's permissions and the grant are read again after the locks, because
// they may have changed while waiting. The rule is enforced on revocation only: removing a group member or
// disabling a user can still leave an initiative without an effective manager (only a platform admin does
// that, and platform admins manage every initiative).
func (s *Service) DeleteGrant(ctx context.Context, callerID, grantID string) error {
	grantID, ok := canonicalUUID(grantID)
	if !ok {
		return ErrNotFound
	}
	return s.store.WithTx(ctx, func(tx Tx) error {
		sub, err := subject(ctx, tx, callerID)
		if err != nil {
			return err
		}
		g, err := tx.Grants().GetGrant(ctx, grantID)
		if err != nil {
			return err
		}
		if _, err := authorize(sub, adminz.GrantDelete, grantResource(g)); err != nil {
			return err
		}

		// Lock order, always: initiative row, then its manager grants by id.
		if err := tx.Grants().LockInitiative(ctx, g.InitiativeID); err != nil {
			return err
		}
		managers, err := tx.Grants().LockManagerGrants(ctx, g.InitiativeID)
		if err != nil {
			return fmt.Errorf("lock manager grants: %w", err)
		}
		if _, _, err := begin(ctx, tx, callerID, adminz.GrantDelete, grantResource(g)); err != nil {
			return err
		}
		g, err = tx.Grants().GetGrant(ctx, grantID)
		if err != nil {
			return err
		}
		if g.Role.ManagesAccess && isLastEffective(managers, g.ID) {
			return ErrLastManager
		}

		deleted, err := tx.Grants().DeleteGrant(ctx, g.ID)
		if err != nil {
			return fmt.Errorf("delete grant: %w", err)
		}
		if !deleted {
			return ErrNotFound
		}
		return record(ctx, tx, event{
			actor: callerID, action: adminz.GrantDelete, targetType: TargetGrant, targetID: g.ID,
			initiativeID: g.InitiativeID, before: docOfGrant(g),
		})
	})
}

func grantResource(g Grant) adminz.Resource {
	return adminz.Resource{Type: TargetGrant, ID: g.ID, InitiativeID: g.InitiativeID}
}

// isLastEffective reports whether grantID is effective and no other manages_access grant is.
func isLastEffective(managers []ManagerGrant, grantID string) bool {
	targetEffective, others := false, 0
	for _, m := range managers {
		switch {
		case m.ID == grantID:
			targetEffective = m.Effective
		case m.Effective:
			others++
		}
	}
	return targetEffective && others == 0
}
