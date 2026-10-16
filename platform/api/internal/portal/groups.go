package portal

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"platform/api/internal/adminz"
)

// keyPattern mirrors the key CHECK of groups, initiatives and initiative roles.
var keyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`)

type groupDoc struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type memberDoc struct {
	GroupID string `json:"group_id"`
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
}

// ListGroups lists the groups (id, key, name): platform admins and initiative managers may, to grant access to
// a group. Members are never part of it. Oldest first, keyset on the group id.
func (s *Service) ListGroups(ctx context.Context, callerID string, req PageRequest) (Page[Group], error) {
	size := pageSize(req.Limit)
	var out Page[Group]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[Group]{}
		_, d, err := begin(ctx, tx, callerID, adminz.GroupList, adminz.Resource{Type: TargetGroup})
		if err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindGroups, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Groups().ListGroups(ctx, d.Scope, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list groups: %w", err)
		}
		out = pageOf(rows, size, cursorKindGroups, func(g Group) Cursor { return Cursor{ID: g.ID} })
		return nil
	})
	return out, err
}

// CreateGroup creates a group (platform admins only). A repeated key is ErrGroupKeyTaken.
func (s *Service) CreateGroup(ctx context.Context, callerID string, in NewGroup) (Group, error) {
	var out Group
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Group{}
		if _, _, err := begin(ctx, tx, callerID, adminz.GroupCreate, adminz.Resource{Type: TargetGroup}); err != nil {
			return err
		}
		var v ValidationError
		switch {
		case in.Key == "":
			v.Problems = append(v.Problems, FieldProblem{Field: "key", Code: CodeRequired})
		case len(in.Key) > maxGroupKey || !cleanText(in.Key) || !keyPattern.MatchString(in.Key):
			v.Problems = append(v.Problems, FieldProblem{Field: "key", Code: CodeInvalid})
		}
		if len(in.Name) > maxGroupName || !cleanText(in.Name) {
			v.Problems = append(v.Problems, FieldProblem{Field: "name", Code: CodeInvalid})
		}
		if len(v.Problems) > 0 {
			return &v
		}
		g, err := tx.Groups().InsertGroup(ctx, in.Key, in.Name)
		if errors.Is(err, ErrDuplicate) {
			return ErrGroupKeyTaken
		}
		if err != nil {
			return fmt.Errorf("insert group: %w", err)
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.GroupCreate, targetType: TargetGroup, targetID: g.ID,
			after: groupDoc{Key: g.Key, Name: g.Name},
		}); err != nil {
			return err
		}
		out = g
		return nil
	})
	return out, err
}

// DeleteGroup deletes a group that has no members and no grants (ErrGroupInUse otherwise). The explicit check
// matters: the foreign keys cascade, so deleting a group in use would silently remove its members and grants.
// The group row is locked first; creating a grant or a member takes a key-share lock on it through its foreign
// key, so a concurrent insert either commits before the count (and the delete is refused) or fails after it.
func (s *Service) DeleteGroup(ctx context.Context, callerID, groupID string) error {
	groupID, ok := canonicalUUID(groupID)
	if !ok {
		return ErrNotFound
	}
	return s.store.WithTx(ctx, func(tx Tx) error {
		if _, _, err := begin(ctx, tx, callerID, adminz.GroupDelete, adminz.Resource{Type: TargetGroup, ID: groupID}); err != nil {
			return err
		}
		g, err := tx.Groups().LockGroup(ctx, groupID)
		if err != nil {
			return err
		}
		members, grants, err := tx.Groups().GroupUsage(ctx, g.ID)
		if err != nil {
			return fmt.Errorf("count group usage: %w", err)
		}
		if members > 0 || grants > 0 {
			return ErrGroupInUse
		}
		if err := tx.Groups().DeleteGroup(ctx, g.ID); err != nil {
			return fmt.Errorf("delete group: %w", err)
		}
		return record(ctx, tx, event{
			actor: callerID, action: adminz.GroupDelete, targetType: TargetGroup, targetID: g.ID,
			before: groupDoc{Key: g.Key, Name: g.Name},
		})
	})
}

// ListGroupMembers lists who is in a group (platform admins only), by user id.
func (s *Service) ListGroupMembers(ctx context.Context, callerID, groupID string, req PageRequest) (Page[GroupMember], error) {
	groupID, ok := canonicalUUID(groupID)
	if !ok {
		return Page[GroupMember]{}, ErrNotFound
	}
	size := pageSize(req.Limit)
	var out Page[GroupMember]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[GroupMember]{}
		_, d, err := begin(ctx, tx, callerID, adminz.GroupMemberList, adminz.Resource{Type: TargetGroupMember, ID: groupID})
		if err != nil {
			return err
		}
		if _, err := tx.Groups().GetGroup(ctx, groupID); err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindMembers, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Groups().ListMembers(ctx, d.Scope, groupID, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list group members: %w", err)
		}
		out = pageOf(rows, size, cursorKindMembers, func(m GroupMember) Cursor { return Cursor{ID: m.UserID} })
		return nil
	})
	return out, err
}

// AddGroupMember adds the one active user with that email to the group (platform admins only: groups are
// global, so a change affects every initiative that grants to them). Repeating it is ErrMemberExists.
func (s *Service) AddGroupMember(ctx context.Context, callerID, groupID, email string) (GroupMember, error) {
	groupID, ok := canonicalUUID(groupID)
	if !ok {
		return GroupMember{}, ErrNotFound
	}
	var out GroupMember
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = GroupMember{}
		if _, _, err := begin(ctx, tx, callerID, adminz.GroupMemberAdd, adminz.Resource{Type: TargetGroupMember, ID: groupID}); err != nil {
			return err
		}
		if _, err := tx.Groups().GetGroup(ctx, groupID); err != nil {
			return err
		}
		u, err := resolveActiveUser(ctx, tx, normalizeEmail(email))
		if err != nil {
			return err
		}
		addedAt, err := tx.Groups().InsertMember(ctx, groupID, u.ID)
		if err != nil {
			var ref *ReferenceError
			switch {
			case errors.Is(err, ErrDuplicate):
				return ErrMemberExists
			case errors.As(err, &ref): // the group or the user vanished meanwhile
				return ErrNotFound
			default:
				return fmt.Errorf("insert group member: %w", err)
			}
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.GroupMemberAdd, targetType: TargetGroupMember, targetID: groupID,
			after: memberDoc{GroupID: groupID, UserID: u.ID, Email: u.Email},
		}); err != nil {
			return err
		}
		out = GroupMember{UserID: u.ID, Email: u.Email, Name: u.Name, Status: u.Status, AddedAt: addedAt}
		return nil
	})
	return out, err
}

// RemoveGroupMember removes a user from a group (platform admins only); a non-member is ErrNotFound.
func (s *Service) RemoveGroupMember(ctx context.Context, callerID, groupID, userID string) error {
	groupID, okGroup := canonicalUUID(groupID)
	userID, okUser := canonicalUUID(userID)
	if !okGroup || !okUser {
		return ErrNotFound
	}
	return s.store.WithTx(ctx, func(tx Tx) error {
		if _, _, err := begin(ctx, tx, callerID, adminz.GroupMemberRemove, adminz.Resource{Type: TargetGroupMember, ID: groupID}); err != nil {
			return err
		}
		u, err := tx.Users().GetUser(ctx, userID)
		if err != nil {
			return err
		}
		removed, err := tx.Groups().DeleteMember(ctx, groupID, userID)
		if err != nil {
			return fmt.Errorf("delete group member: %w", err)
		}
		if !removed {
			return ErrNotFound
		}
		return record(ctx, tx, event{
			actor: callerID, action: adminz.GroupMemberRemove, targetType: TargetGroupMember, targetID: groupID,
			before: memberDoc{GroupID: groupID, UserID: u.ID, Email: u.Email},
		})
	})
}
