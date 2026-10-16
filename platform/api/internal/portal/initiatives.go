package portal

import (
	"context"
	"errors"
	"fmt"

	"platform/api/internal/adminz"
)

// Bounds of the free-text inputs of the catalog. The schema only limits the role key (32); the others follow
// the groups.
const (
	maxInitiativeKey  = 63
	maxInitiativeName = 200
	maxAppName        = 200
	maxRoleKey        = 32 // initiative_roles_key_length
)

// initiativeDoc is the audit document of an initiative.
type initiativeDoc struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func docOfInitiative(i Initiative) initiativeDoc {
	return initiativeDoc{Key: i.Key, Name: i.Name, Status: i.Status}
}

// validKey reports whether key is a well-formed key (the schema pattern) of at most max bytes.
func validKey(key string, max int) bool {
	return len(key) <= max && cleanText(key) && keyPattern.MatchString(key)
}

// ListInitiatives lists the initiatives the caller may see: all of them for platform admins, the managed ones
// for managers (anyone else is forbidden). Oldest first, keyset on the id.
func (s *Service) ListInitiatives(ctx context.Context, callerID string, req PageRequest) (Page[Initiative], error) {
	size := pageSize(req.Limit)
	var out Page[Initiative]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[Initiative]{}
		_, d, err := begin(ctx, tx, callerID, adminz.InitiativeList, adminz.Resource{Type: TargetInitiative})
		if err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindInitiatives, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Catalog().ListInitiatives(ctx, d.Scope, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list initiatives: %w", err)
		}
		out = pageOf(rows, size, cursorKindInitiatives, func(i Initiative) Cursor { return Cursor{ID: i.ID} })
		return nil
	})
	return out, err
}

// GetInitiative returns one initiative: platform admins, and the managers of it (anyone else gets ErrNotFound).
func (s *Service) GetInitiative(ctx context.Context, callerID, initiativeID string) (Initiative, error) {
	initiativeID, ok := canonicalUUID(initiativeID)
	if !ok {
		return Initiative{}, ErrNotFound
	}
	var out Initiative
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Initiative{}
		res := adminz.Resource{Type: TargetInitiative, ID: initiativeID, InitiativeID: initiativeID}
		if _, _, err := begin(ctx, tx, callerID, adminz.InitiativeRead, res); err != nil {
			return err
		}
		i, err := tx.Catalog().GetInitiative(ctx, initiativeID)
		if err != nil {
			return err
		}
		out = i
		return nil
	})
	return out, err
}

// CreateInitiative creates an initiative (platform admins only). The key is immutable afterwards. A repeated
// key is ErrInitiativeKeyTaken and creates no second row.
func (s *Service) CreateInitiative(ctx context.Context, callerID string, in NewInitiative) (Initiative, error) {
	var out Initiative
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Initiative{}
		if _, _, err := begin(ctx, tx, callerID, adminz.InitiativeCreate, adminz.Resource{Type: TargetInitiative}); err != nil {
			return err
		}
		var v ValidationError
		switch {
		case in.Key == "":
			v.Problems = append(v.Problems, FieldProblem{Field: "key", Code: CodeRequired})
		case !validKey(in.Key, maxInitiativeKey):
			v.Problems = append(v.Problems, FieldProblem{Field: "key", Code: CodeInvalid})
		}
		if len(in.Name) > maxInitiativeName || !cleanText(in.Name) {
			v.Problems = append(v.Problems, FieldProblem{Field: "name", Code: CodeInvalid})
		}
		if len(v.Problems) > 0 {
			return &v
		}
		i, err := tx.Catalog().InsertInitiative(ctx, in.Key, in.Name)
		if errors.Is(err, ErrDuplicate) {
			return ErrInitiativeKeyTaken
		}
		if err != nil {
			return fmt.Errorf("insert initiative: %w", err)
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.InitiativeCreate, targetType: TargetInitiative, targetID: i.ID,
			initiativeID: i.ID, after: docOfInitiative(i),
		}); err != nil {
			return err
		}
		out = i
		return nil
	})
	return out, err
}

// initiativeAction is the action a patch asks for: a status change is an activation or a deactivation, anything
// else an update. A status that is neither is left to the validation.
func initiativeAction(p InitiativePatch) adminz.Action {
	if p.Status != nil {
		switch *p.Status {
		case InitiativeStatusInactive:
			return adminz.InitiativeDeactivate
		case InitiativeStatusActive:
			return adminz.InitiativeActivate
		}
	}
	return adminz.InitiativeUpdate
}

func validateInitiativePatch(p InitiativePatch) error {
	var v ValidationError
	if p.Name == nil && p.Status == nil {
		v.Problems = append(v.Problems, FieldProblem{Field: "name", Code: CodeAtLeastOne}, FieldProblem{Field: "status", Code: CodeAtLeastOne})
	}
	if p.Name != nil && (len(*p.Name) > maxInitiativeName || !cleanText(*p.Name)) {
		v.Problems = append(v.Problems, FieldProblem{Field: "name", Code: CodeInvalid})
	}
	if p.Status != nil {
		switch *p.Status {
		case InitiativeStatusActive, InitiativeStatusInactive:
		case "":
			v.Problems = append(v.Problems, FieldProblem{Field: "status", Code: CodeRequired})
		default:
			v.Problems = append(v.Problems, FieldProblem{Field: "status", Code: CodeInvalid})
		}
	}
	if len(v.Problems) > 0 {
		return &v
	}
	return nil
}

// UpdateInitiative renames an initiative and/or activates or deactivates it (platform admins only). The key is
// immutable. The `platform` initiative cannot be deactivated (ErrPlatformInitiative): its apps include the
// portal, and with the initiative inactive nobody could open the portal to repair it. A patch that changes
// nothing writes no audit row and returns the initiative as it is.
//
// The initiative row is locked FOR NO KEY UPDATE first, the first lock of every path that touches an
// initiative (see DeleteGrant), so a deactivation waits for a grant revocation or a role change in progress.
func (s *Service) UpdateInitiative(ctx context.Context, callerID, initiativeID string, p InitiativePatch) (Initiative, error) {
	initiativeID, ok := canonicalUUID(initiativeID)
	if !ok {
		return Initiative{}, ErrNotFound
	}
	var out Initiative
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Initiative{}
		res := adminz.Resource{Type: TargetInitiative, ID: initiativeID, InitiativeID: initiativeID}
		if _, _, err := begin(ctx, tx, callerID, initiativeAction(p), res); err != nil {
			return err
		}
		if err := validateInitiativePatch(p); err != nil {
			return err
		}
		if err := tx.Grants().LockInitiative(ctx, initiativeID); err != nil {
			return err
		}
		if _, _, err := begin(ctx, tx, callerID, initiativeAction(p), res); err != nil {
			return err
		}
		cur, err := tx.Catalog().GetInitiative(ctx, initiativeID)
		if err != nil {
			return err
		}
		if p.Status != nil && *p.Status == InitiativeStatusInactive && cur.Key == PlatformInitiativeKey {
			return ErrPlatformInitiative
		}
		next := cur
		if p.Name != nil {
			next.Name = *p.Name
		}
		if p.Status != nil {
			next.Status = *p.Status
		}
		if next.Name == cur.Name && next.Status == cur.Status {
			out = cur
			return nil
		}
		if err := tx.Catalog().UpdateInitiative(ctx, cur.ID, next.Name, next.Status); err != nil {
			return fmt.Errorf("update initiative: %w", err)
		}
		action := adminz.InitiativeUpdate
		if next.Status != cur.Status {
			action = initiativeAction(InitiativePatch{Status: &next.Status})
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: action, targetType: TargetInitiative, targetID: cur.ID,
			initiativeID: cur.ID, before: docOfInitiative(cur), after: docOfInitiative(next),
		}); err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}
