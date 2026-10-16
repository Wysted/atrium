package portal

import (
	"context"
	"errors"
	"fmt"

	"platform/api/internal/adminz"
)

// roleDoc is the audit document of a role.
type roleDoc struct {
	Key           string `json:"key"`
	ManagesAccess bool   `json:"manages_access"`
}

func docOfRole(r Role) roleDoc { return roleDoc{Key: r.Key, ManagesAccess: r.ManagesAccess} }

func roleResource(r Role) adminz.Resource {
	return adminz.Resource{Type: TargetRole, ID: r.ID, InitiativeID: r.InitiativeID}
}

// ListRoles lists the roles of an initiative (platform admins and the managers of it, who need them to grant),
// oldest first.
func (s *Service) ListRoles(ctx context.Context, callerID, initiativeID string, req PageRequest) (Page[Role], error) {
	initiativeID, ok := canonicalUUID(initiativeID)
	if !ok {
		return Page[Role]{}, ErrNotFound
	}
	size := pageSize(req.Limit)
	var out Page[Role]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[Role]{}
		_, d, err := begin(ctx, tx, callerID, adminz.RoleList, adminz.Resource{Type: TargetRole, InitiativeID: initiativeID})
		if err != nil {
			return err
		}
		if _, err := tx.Catalog().GetInitiative(ctx, initiativeID); err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindRoles, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Catalog().ListRoles(ctx, d.Scope, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list roles: %w", err)
		}
		out = pageOf(rows, size, cursorKindRoles, func(r Role) Cursor { return Cursor{ID: r.ID} })
		return nil
	})
	return out, err
}

// CreateRole creates a role in an initiative (platform admins only). A repeated key is ErrRoleKeyTaken. The key
// is immutable afterwards: role keys are part of the apps' contract (the `roles` claim).
func (s *Service) CreateRole(ctx context.Context, callerID, initiativeID string, in NewRole) (Role, error) {
	initiativeID, ok := canonicalUUID(initiativeID)
	if !ok {
		return Role{}, ErrNotFound
	}
	var out Role
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Role{}
		if _, _, err := begin(ctx, tx, callerID, adminz.RoleCreate, adminz.Resource{Type: TargetRole, InitiativeID: initiativeID}); err != nil {
			return err
		}
		if _, err := tx.Catalog().GetInitiative(ctx, initiativeID); err != nil {
			return err
		}
		switch {
		case in.Key == "":
			return invalid("key", CodeRequired)
		case !validKey(in.Key, maxRoleKey):
			return invalid("key", CodeInvalid)
		}
		r, err := tx.Catalog().InsertRole(ctx, initiativeID, in.Key, in.ManagesAccess)
		var ref *ReferenceError
		switch {
		case errors.Is(err, ErrDuplicate):
			return ErrRoleKeyTaken
		case errors.As(err, &ref): // the initiative vanished meanwhile
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("insert role: %w", err)
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.RoleCreate, targetType: TargetRole, targetID: r.ID,
			initiativeID: initiativeID, after: docOfRole(r),
		}); err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}

// UpdateRole toggles manages_access of a role (platform admins only); the key is immutable. Turning it off
// removes every grant of the role from the managers, so it follows the last-manager rule of DeleteGrant: it
// fails with ErrLastManager when the role holds an effective manager grant and no other effective one would
// remain in the initiative. A role that holds no effective grant removes no manager and can always be turned
// off. Setting the value the role already has changes nothing and writes no audit row.
//
// Lock order, as in DeleteGrant: the initiative row (FOR NO KEY UPDATE), then the initiative's manages_access
// grants by id. The role and the caller's permissions are read again after the locks. Without them, two
// administrators turning off the two manager roles of an initiative at the same time would both count the
// other role as the remaining manager and both succeed.
func (s *Service) UpdateRole(ctx context.Context, callerID, roleID string, p RolePatch) (Role, error) {
	roleID, ok := canonicalUUID(roleID)
	if !ok {
		return Role{}, ErrNotFound
	}
	var out Role
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Role{}
		sub, err := subject(ctx, tx, callerID)
		if err != nil {
			return err
		}
		r, err := tx.Catalog().GetRole(ctx, roleID)
		if err != nil {
			return err
		}
		if _, err := authorize(sub, adminz.RoleUpdate, roleResource(r)); err != nil {
			return err
		}
		if p.ManagesAccess == nil {
			return invalid("manages_access", CodeRequired)
		}

		if err := tx.Grants().LockInitiative(ctx, r.InitiativeID); err != nil {
			return err
		}
		managers, err := tx.Grants().LockManagerGrants(ctx, r.InitiativeID)
		if err != nil {
			return fmt.Errorf("lock manager grants: %w", err)
		}
		if _, _, err := begin(ctx, tx, callerID, adminz.RoleUpdate, roleResource(r)); err != nil {
			return err
		}
		r, err = tx.Catalog().GetRole(ctx, roleID)
		if err != nil {
			return err
		}
		if r.ManagesAccess == *p.ManagesAccess {
			out = r
			return nil
		}
		if !*p.ManagesAccess && removesLastManager(managers, r.ID) {
			return ErrLastManager
		}
		if err := tx.Catalog().SetRoleManagesAccess(ctx, r.ID, *p.ManagesAccess); err != nil {
			return fmt.Errorf("set role manages_access: %w", err)
		}
		next := r
		next.ManagesAccess = *p.ManagesAccess
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.RoleUpdate, targetType: TargetRole, targetID: r.ID,
			initiativeID: r.InitiativeID, before: docOfRole(r), after: docOfRole(next),
		}); err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

// removesLastManager reports whether the grants of roleID include an effective one and no grant of another role
// is effective: demoting the role would leave the initiative without an effective manager.
func removesLastManager(managers []ManagerGrant, roleID string) bool {
	roleEffective, others := false, 0
	for _, m := range managers {
		switch {
		case m.RoleID == roleID:
			roleEffective = roleEffective || m.Effective
		case m.Effective:
			others++
		}
	}
	return roleEffective && others == 0
}

// DeleteRole deletes a role that has no grants (platform admins only); ErrRoleInUse otherwise. The foreign key
// from the grants is the guard: a grant created at the same moment either commits first (the delete is
// refused) or fails against the deleted role. Repeating a deletion answers ErrNotFound.
//
// Any path that updates or deletes a role row locks the initiative first (FOR NO KEY UPDATE), like UpdateRole:
// the DELETE's foreign-key check takes FOR KEY SHARE locks on the initiative's grants, which would otherwise
// cross the FOR UPDATE that UpdateRole takes on them (deadlock). The role and the caller's permissions are read
// again after the lock; a role deleted meanwhile is ErrNotFound.
func (s *Service) DeleteRole(ctx context.Context, callerID, roleID string) error {
	roleID, ok := canonicalUUID(roleID)
	if !ok {
		return ErrNotFound
	}
	return s.store.WithTx(ctx, func(tx Tx) error {
		sub, err := subject(ctx, tx, callerID)
		if err != nil {
			return err
		}
		r, err := tx.Catalog().GetRole(ctx, roleID)
		if err != nil {
			return err
		}
		if _, err := authorize(sub, adminz.RoleDelete, roleResource(r)); err != nil {
			return err
		}
		if err := tx.Grants().LockInitiative(ctx, r.InitiativeID); err != nil {
			return err
		}
		if _, _, err := begin(ctx, tx, callerID, adminz.RoleDelete, roleResource(r)); err != nil {
			return err
		}
		if r, err = tx.Catalog().GetRole(ctx, roleID); err != nil {
			return err
		}
		deleted, err := tx.Catalog().DeleteRole(ctx, r.ID)
		var ref *ReferenceError
		switch {
		case errors.As(err, &ref):
			return ErrRoleInUse
		case err != nil:
			return fmt.Errorf("delete role: %w", err)
		case !deleted:
			return ErrNotFound
		}
		return record(ctx, tx, event{
			actor: callerID, action: adminz.RoleDelete, targetType: TargetRole, targetID: r.ID,
			initiativeID: r.InitiativeID, before: docOfRole(r),
		})
	})
}
