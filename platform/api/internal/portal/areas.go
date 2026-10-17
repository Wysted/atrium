package portal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/adminz"
)

// Areas (docs/reference/api.md, "Area supervisors"): the administration of areas, their members and their supervisors,
// by platform admins only. What the supervisors reach through them is in tokens.go and robots.go.

// Bounds of the free-text inputs of an area (the key CHECK of areas mirrors maxAreaKey).
const (
	maxAreaKey  = 63
	maxAreaName = 200
)

// Cursor kinds of the area listings.
const (
	cursorKindAreas       = "areas"
	cursorKindAreaMembers = "area_members"
	cursorKindAreaSupers  = "area_supervisors"
)

// TargetArea is the audit target of every area event: the area id, whatever changed in it.
const TargetArea = "area"

// The conflicts of the area use cases.
var (
	ErrAreaKeyTaken         = &Conflict{Detail: "an area with this key already exists"}
	ErrAreaInUse            = &Conflict{Detail: "the area still has members or supervisors"}
	ErrAreaMemberExists     = &Conflict{Detail: "the user already belongs to an area"}
	ErrAreaSupervisorExists = &Conflict{Detail: "the user already supervises this area"}
)

// Area is a container of people whose credentials its supervisors administer. Areas are global.
type Area struct {
	ID        string
	Key       string
	Name      string
	CreatedAt time.Time
}

// NewArea is the request to create an area.
type NewArea struct {
	Key  string
	Name string
}

// AreaRef identifies an area in a response.
type AreaRef struct {
	ID   string
	Key  string
	Name string
}

// AreaPerson is a member or a supervisor of an area.
type AreaPerson struct {
	UserID  string
	Email   string
	Name    string
	Status  string
	AddedAt time.Time
}

// AreaRelation says which people of an area a repository method works on.
type AreaRelation string

const (
	AreaMembers     AreaRelation = "members"
	AreaSupervisors AreaRelation = "supervisors"
)

// AreaRepo is the storage of areas, their members and their supervisors.
type AreaRepo interface {
	ListAreas(ctx context.Context, scope adminz.Scope, q PageQuery) ([]Area, error)
	// GetArea returns ErrNotFound for an unknown id.
	GetArea(ctx context.Context, id string) (Area, error)
	// InsertArea returns ErrDuplicate when the key exists.
	InsertArea(ctx context.Context, key, name string) (Area, error)
	// DeleteArea reports whether a row was deleted. It takes no lock and relies on ON DELETE RESTRICT: ErrInUse
	// means the area still has members or supervisors.
	DeleteArea(ctx context.Context, id string) (bool, error)
	// ListPeople lists the members or the supervisors of an area by user id. Only Scope.All lists them.
	ListPeople(ctx context.Context, rel AreaRelation, scope adminz.Scope, areaID string, q PageQuery) ([]AreaPerson, error)
	// InsertPerson returns when the user was added; ErrDuplicate for an existing row (a member of any area, or a
	// supervisor of this one) and a *ReferenceError for an area deleted meanwhile or an unknown or non-human user.
	InsertPerson(ctx context.Context, rel AreaRelation, areaID, userID string) (time.Time, error)
	// DeletePerson reports whether a row was deleted.
	DeletePerson(ctx context.Context, rel AreaRelation, areaID, userID string) (bool, error)
	// SupervisableArea is the area of a person who is supervisable: a member of an area who is not a platform admin
	// and supervises no area, read from the rows whatever the statuses (fail closed). "" when the person is not
	// (an unknown id included). It reads inside the transaction of the use case, never cached.
	SupervisableArea(ctx context.Context, personID string) (string, error)
	// ListSupervisableMembers lists the supervisable members of an area by user id (what a supervisor sees).
	ListSupervisableMembers(ctx context.Context, areaID string, q PageQuery) ([]AreaPerson, error)
}

type areaDoc struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type areaPersonDoc struct {
	AreaID string `json:"area_id"`
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

// areaRelation is what differs between the members and the supervisors of an area.
type areaRelation struct {
	rel               AreaRelation
	add, remove, list adminz.Action
	cursor            string
	exists            *Conflict
}

var (
	memberRelation = areaRelation{
		rel: AreaMembers, add: adminz.AreaMemberAdd, remove: adminz.AreaMemberRemove, list: adminz.AreaMemberList,
		cursor: cursorKindAreaMembers, exists: ErrAreaMemberExists,
	}
	supervisorRelation = areaRelation{
		rel: AreaSupervisors, add: adminz.AreaSupervisorAdd, remove: adminz.AreaSupervisorRemove, list: adminz.AreaSupervisorList,
		cursor: cursorKindAreaSupers, exists: ErrAreaSupervisorExists,
	}
)

// ListAreas lists the areas (platform admins only), oldest first, keyset on the area id.
func (s *Service) ListAreas(ctx context.Context, callerID string, req PageRequest) (Page[Area], error) {
	size := pageSize(req.Limit)
	var out Page[Area]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[Area]{}
		_, d, err := begin(ctx, tx, callerID, adminz.AreaList, adminz.Resource{Type: TargetArea})
		if err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindAreas, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Areas().ListAreas(ctx, d.Scope, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list areas: %w", err)
		}
		out = pageOf(rows, size, cursorKindAreas, func(a Area) Cursor { return Cursor{ID: a.ID} })
		return nil
	})
	return out, err
}

// GetArea returns one area (platform admins only). An unknown or malformed id is ErrNotFound.
func (s *Service) GetArea(ctx context.Context, callerID, areaID string) (Area, error) {
	var out Area
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Area{}
		if _, _, err := begin(ctx, tx, callerID, adminz.AreaList, adminz.Resource{Type: TargetArea}); err != nil {
			return err
		}
		id, ok := canonicalUUID(areaID)
		if !ok {
			return ErrNotFound
		}
		a, err := tx.Areas().GetArea(ctx, id)
		if err != nil {
			return err
		}
		out = a
		return nil
	})
	return out, err
}

// CreateArea creates an area (platform admins only). A repeated key is ErrAreaKeyTaken.
func (s *Service) CreateArea(ctx context.Context, callerID string, in NewArea) (Area, error) {
	var out Area
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Area{}
		if _, _, err := begin(ctx, tx, callerID, adminz.AreaCreate, adminz.Resource{Type: TargetArea}); err != nil {
			return err
		}
		var v ValidationError
		switch {
		case in.Key == "":
			v.Problems = append(v.Problems, FieldProblem{Field: "key", Code: CodeRequired})
		case len(in.Key) > maxAreaKey || !cleanText(in.Key) || !keyPattern.MatchString(in.Key):
			v.Problems = append(v.Problems, FieldProblem{Field: "key", Code: CodeInvalid})
		}
		if len(in.Name) > maxAreaName || !cleanText(in.Name) {
			v.Problems = append(v.Problems, FieldProblem{Field: "name", Code: CodeInvalid})
		}
		if len(v.Problems) > 0 {
			return &v
		}
		a, err := tx.Areas().InsertArea(ctx, in.Key, in.Name)
		if errors.Is(err, ErrDuplicate) {
			return ErrAreaKeyTaken
		}
		if err != nil {
			return fmt.Errorf("insert area: %w", err)
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.AreaCreate, targetType: TargetArea, targetID: a.ID,
			after: areaDoc{Key: a.Key, Name: a.Name},
		}); err != nil {
			return err
		}
		out = a
		return nil
	})
	return out, err
}

// DeleteArea deletes an area that has no members and no supervisors (ErrAreaInUse otherwise). It takes no lock:
// the foreign keys are ON DELETE RESTRICT, so a member or supervisor added concurrently either commits first (and
// the delete fails with ErrAreaInUse) or fails afterwards (and the add answers ErrNotFound).
func (s *Service) DeleteArea(ctx context.Context, callerID, areaID string) error {
	areaID, ok := canonicalUUID(areaID)
	if !ok {
		return ErrNotFound
	}
	return s.store.WithTx(ctx, func(tx Tx) error {
		if _, _, err := begin(ctx, tx, callerID, adminz.AreaDelete, adminz.Resource{Type: TargetArea, ID: areaID}); err != nil {
			return err
		}
		a, err := tx.Areas().GetArea(ctx, areaID)
		if err != nil {
			return err
		}
		deleted, err := tx.Areas().DeleteArea(ctx, a.ID)
		if errors.Is(err, ErrInUse) {
			return ErrAreaInUse
		}
		if err != nil {
			return fmt.Errorf("delete area: %w", err)
		}
		if !deleted { // deleted concurrently
			return ErrNotFound
		}
		return record(ctx, tx, event{
			actor: callerID, action: adminz.AreaDelete, targetType: TargetArea, targetID: a.ID,
			before: areaDoc{Key: a.Key, Name: a.Name},
		})
	})
}

// ListAreaMembers lists the members of an area by user id: all of them for platform admins, the supervisable ones
// for a supervisor of the area.
func (s *Service) ListAreaMembers(ctx context.Context, callerID, areaID string, req PageRequest) (Page[AreaPerson], error) {
	return s.listAreaPeople(ctx, memberRelation, callerID, areaID, req)
}

// ListAreaSupervisors lists the supervisors of an area (platform admins only), by user id.
func (s *Service) ListAreaSupervisors(ctx context.Context, callerID, areaID string, req PageRequest) (Page[AreaPerson], error) {
	return s.listAreaPeople(ctx, supervisorRelation, callerID, areaID, req)
}

// AddAreaMember puts the one active human with that email in the area (platform admins only). A person belongs to
// at most one area: being in this one or another is ErrAreaMemberExists (moving someone is remove, then add).
func (s *Service) AddAreaMember(ctx context.Context, callerID, areaID, email string) (AreaPerson, error) {
	return s.addAreaPerson(ctx, memberRelation, callerID, areaID, email)
}

// AddAreaSupervisor makes the one active human with that email a supervisor of the area (platform admins only).
func (s *Service) AddAreaSupervisor(ctx context.Context, callerID, areaID, email string) (AreaPerson, error) {
	return s.addAreaPerson(ctx, supervisorRelation, callerID, areaID, email)
}

// RemoveAreaMember takes a person out of the area (platform admins only); a non-member is ErrNotFound.
func (s *Service) RemoveAreaMember(ctx context.Context, callerID, areaID, userID string) error {
	return s.removeAreaPerson(ctx, memberRelation, callerID, areaID, userID)
}

// RemoveAreaSupervisor removes a supervisor from the area (platform admins only); a non-supervisor is ErrNotFound.
func (s *Service) RemoveAreaSupervisor(ctx context.Context, callerID, areaID, userID string) error {
	return s.removeAreaPerson(ctx, supervisorRelation, callerID, areaID, userID)
}

func (s *Service) listAreaPeople(ctx context.Context, r areaRelation, callerID, areaID string, req PageRequest) (Page[AreaPerson], error) {
	areaID, ok := canonicalUUID(areaID)
	if !ok {
		return Page[AreaPerson]{}, ErrNotFound
	}
	size := pageSize(req.Limit)
	var out Page[AreaPerson]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[AreaPerson]{}
		// The area is the resource and its own scope: a supervisor of it passes, with the members they can act on only.
		_, d, err := begin(ctx, tx, callerID, r.list, adminz.Resource{Type: TargetArea, ID: areaID, AreaID: areaID})
		if err != nil {
			return err
		}
		if _, err := tx.Areas().GetArea(ctx, areaID); err != nil {
			return err
		}
		after, err := decodeCursor(r.cursor, req.Cursor, false)
		if err != nil {
			return err
		}
		var rows []AreaPerson
		if d.Reason == adminz.ReasonAreaSupervisor {
			rows, err = tx.Areas().ListSupervisableMembers(ctx, areaID, PageQuery{After: after, Limit: size + 1})
		} else {
			rows, err = tx.Areas().ListPeople(ctx, r.rel, d.Scope, areaID, PageQuery{After: after, Limit: size + 1})
		}
		if err != nil {
			return fmt.Errorf("list area %s: %w", r.rel, err)
		}
		out = pageOf(rows, size, r.cursor, func(p AreaPerson) Cursor { return Cursor{ID: p.UserID} })
		return nil
	})
	return out, err
}

func (s *Service) addAreaPerson(ctx context.Context, r areaRelation, callerID, areaID, email string) (AreaPerson, error) {
	areaID, ok := canonicalUUID(areaID)
	if !ok {
		return AreaPerson{}, ErrNotFound
	}
	var out AreaPerson
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = AreaPerson{}
		if _, _, err := begin(ctx, tx, callerID, r.add, adminz.Resource{Type: TargetArea, ID: areaID}); err != nil {
			return err
		}
		if _, err := tx.Areas().GetArea(ctx, areaID); err != nil {
			return err
		}
		u, err := resolveActiveUser(ctx, tx, normalizeEmail(email))
		if err != nil {
			return err
		}
		addedAt, err := tx.Areas().InsertPerson(ctx, r.rel, areaID, u.ID)
		if err != nil {
			switch {
			case errors.Is(err, ErrDuplicate):
				return r.exists
			case isReferenceError(err): // the area or the user vanished meanwhile
				return ErrNotFound
			default:
				return fmt.Errorf("insert area %s: %w", r.rel, err)
			}
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: r.add, targetType: TargetArea, targetID: areaID,
			after: areaPersonDoc{AreaID: areaID, UserID: u.ID, Email: u.Email},
		}); err != nil {
			return err
		}
		out = AreaPerson{UserID: u.ID, Email: u.Email, Name: u.Name, Status: u.Status, AddedAt: addedAt}
		return nil
	})
	return out, err
}

func (s *Service) removeAreaPerson(ctx context.Context, r areaRelation, callerID, areaID, userID string) error {
	areaID, okArea := canonicalUUID(areaID)
	userID, okUser := canonicalUUID(userID)
	if !okArea || !okUser {
		return ErrNotFound
	}
	return s.store.WithTx(ctx, func(tx Tx) error {
		if _, _, err := begin(ctx, tx, callerID, r.remove, adminz.Resource{Type: TargetArea, ID: areaID}); err != nil {
			return err
		}
		u, err := tx.Users().GetUser(ctx, userID)
		if err != nil {
			return err
		}
		removed, err := tx.Areas().DeletePerson(ctx, r.rel, areaID, userID)
		if err != nil {
			return fmt.Errorf("delete area %s: %w", r.rel, err)
		}
		if !removed {
			return ErrNotFound
		}
		return record(ctx, tx, event{
			actor: callerID, action: r.remove, targetType: TargetArea, targetID: areaID,
			before: areaPersonDoc{AreaID: areaID, UserID: u.ID, Email: u.Email},
		})
	})
}

func isReferenceError(err error) bool {
	_, ok := errors.AsType[*ReferenceError](err)
	return ok
}
