package portal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wysted/atrium/platform/api/internal/adminz"
)

// Robots (contract block 6, "Non-browser clients"): users of kind 'robot', created and administered by platform
// admins only. Managers may list them (id, name, status) to grant them roles.

// TargetRobot is the audit target type of a robot; AuditRobotCreate and AuditRobotUpdate are the audit actions
// (the same names as the authorization actions).
const (
	TargetRobot                    = "robot"
	AuditRobotCreate adminz.Action = "robot.create"
	AuditRobotUpdate adminz.Action = "robot.update"
)

// maxRobotName is users_robot_name_length, in characters.
const maxRobotName = 64

// The conflicts of the robots.
var (
	ErrRobotNameTaken = &Conflict{Detail: "a robot with this name already exists"}
	ErrRobotDisabled  = &Conflict{Detail: "the robot is disabled"}
)

// Robot is a robot as the API shows it. Detail is false in the listing a manager gets (id, name and status only):
// the other fields are then zero.
type Robot struct {
	ID          string
	Name        string
	Status      string
	Detail      bool
	OwnerUserID string // the accountable human, informational; empty when none
	OwnerEmail  string // of the owner; empty when none (Detail only)
	OwnerName   string // of the owner; empty when none (Detail only)
	CreatedAt   time.Time
	LastUsedAt  *time.Time // the newest last_used_at of its tokens
}

// forManager trims a robot to what a manager sees: id, name and status.
func (r Robot) forManager() Robot { return Robot{ID: r.ID, Name: r.Name, Status: r.Status} }

// NewRobot is the request to create a robot; OwnerUserID is optional.
type NewRobot struct {
	Name        string
	OwnerUserID string
}

// RobotPatch is the change of a robot; nil leaves a field alone. An OwnerUserID that points to "" clears the owner.
type RobotPatch struct {
	Name        *string
	Status      *string
	OwnerUserID *string
}

// RobotRepo is the storage of robots. Every lookup and lock sees robots only: a human id is ErrNotFound (the
// counterpart of UserRepo, which sees humans only).
type RobotRepo interface {
	// ListRobots lists by id; only Scope.All lists them (robots are global).
	ListRobots(ctx context.Context, scope adminz.Scope, q PageQuery) ([]Robot, error)
	// ListRobotsOf lists the robots whose owner is ownerID, by id, with the same fields as ListRobots (the
	// caller has already authorized the owner as a target).
	ListRobotsOf(ctx context.Context, ownerID string, q PageQuery) ([]Robot, error)
	// GetRobot returns ErrNotFound for an unknown id or a human. LastUsedAt is only filled by ListRobots (and
	// LastUsed): the other callers do not return it.
	GetRobot(ctx context.Context, id string) (Robot, error)
	// LastUsed is the newest last_used_at of the robot's tokens (nil: never used).
	LastUsed(ctx context.Context, id string) (*time.Time, error)
	// LockRobot is GetRobot with a row lock (FOR NO KEY UPDATE), the lock SetUserStatus takes on a human: token
	// creation and the disable of the robot serialize on it.
	LockRobot(ctx context.Context, id string) (Robot, error)
	// InsertRobot returns ErrDuplicate when the name is taken (case-insensitive) and a *ReferenceError when the
	// owner does not exist.
	InsertRobot(ctx context.Context, name, ownerID string) (Robot, error)
	// UpdateRobot writes name, status and owner of a locked robot; same errors as InsertRobot.
	UpdateRobot(ctx context.Context, r Robot) error
}

// robotDoc is the audit document of a robot.
type robotDoc struct {
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	OwnerUserID   string   `json:"owner_user_id,omitempty"`
	TokensRevoked []string `json:"tokens_revoked,omitempty"` // ids, so each one can be traced
}

func docOfRobot(r Robot) robotDoc {
	return robotDoc{Name: r.Name, Status: r.Status, OwnerUserID: r.OwnerUserID}
}

// robotNameProblems validates a trimmed name against users_robot_name_length (1 to 64 characters).
func robotNameProblems(name string) []FieldProblem {
	switch {
	case name == "":
		return []FieldProblem{{Field: "name", Code: CodeRequired}}
	case utf8.RuneCountInString(name) > maxRobotName || !cleanText(name):
		return []FieldProblem{{Field: "name", Code: CodeInvalid}}
	}
	return nil
}

// checkRobotOwner requires ownerID to be an active human. Unknown, a robot, disabled: all the same problem.
func checkRobotOwner(ctx context.Context, tx Tx, ownerID string) error {
	u, err := tx.Users().GetUser(ctx, ownerID) // humans only
	if errors.Is(err, ErrNotFound) {
		return invalid("owner_user_id", CodeUnknown)
	}
	if err != nil {
		return fmt.Errorf("get robot owner: %w", err)
	}
	if u.Status != UserStatusActive {
		return invalid("owner_user_id", CodeUnknown)
	}
	return nil
}

// robotWriteError maps the constraint violations of a robot write.
func robotWriteError(what string, err error) error {
	var ref *ReferenceError
	switch {
	case errors.Is(err, ErrDuplicate):
		return ErrRobotNameTaken
	case errors.As(err, &ref): // the owner vanished meanwhile
		return invalid("owner_user_id", CodeUnknown)
	default:
		return fmt.Errorf("%s: %w", what, err)
	}
}

// ListRobots lists the robots by id. Platform admins see every field; managers (who need the list to grant) see
// id, name and status. A non-empty status keeps the robots in that status; the keyset is the same, so a cursor
// stays valid for the same status.
func (s *Service) ListRobots(ctx context.Context, callerID string, req PageRequest, status string) (Page[Robot], error) {
	size := pageSize(req.Limit)
	var out Page[Robot]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[Robot]{}
		sub, d, err := begin(ctx, tx, callerID, adminz.RobotList, adminz.Resource{Type: TargetRobot})
		if err != nil {
			return err
		}
		if status != "" && status != UserStatusActive && status != UserStatusDisabled {
			return invalid("status", CodeInvalid)
		}
		after, err := decodeCursor(cursorKindRobots, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Robots().ListRobots(ctx, d.Scope, PageQuery{After: after, Limit: size + 1, Status: status})
		if err != nil {
			return fmt.Errorf("list robots: %w", err)
		}
		for i := range rows {
			if sub.PlatformAdmin {
				rows[i].Detail = true
			} else {
				rows[i] = rows[i].forManager()
			}
		}
		out = pageOf(rows, size, cursorKindRobots, func(r Robot) Cursor { return Cursor{ID: r.ID} })
		return nil
	})
	return out, err
}

// GetRobot returns one robot with the same authorization and trimming as ListRobots: platform admins see every
// field, managers id, name and status, anyone else is refused. An unknown or human id is ErrNotFound.
func (s *Service) GetRobot(ctx context.Context, callerID, robotID string) (Robot, error) {
	robotID, ok := canonicalUUID(robotID)
	if !ok {
		return Robot{}, ErrNotFound
	}
	var out Robot
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Robot{}
		sub, _, err := begin(ctx, tx, callerID, adminz.RobotList, adminz.Resource{Type: TargetRobot})
		if err != nil {
			return err
		}
		r, err := tx.Robots().GetRobot(ctx, robotID)
		if err != nil {
			return err
		}
		if !sub.PlatformAdmin {
			out = r.forManager()
			return nil
		}
		r.Detail = true
		out = r
		return fillLastUsed(ctx, tx, &out)
	})
	return out, err
}

// CreateRobot creates a robot (platform admins only). The name is 1 to 64 characters, unique among robots
// case-insensitively (ErrRobotNameTaken); the optional owner must be an active human (422).
func (s *Service) CreateRobot(ctx context.Context, callerID string, in NewRobot) (Robot, error) {
	in.OwnerUserID = lowerIfUUID(in.OwnerUserID)
	var out Robot
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Robot{}
		if _, _, err := begin(ctx, tx, callerID, adminz.RobotCreate, adminz.Resource{Type: TargetRobot}); err != nil {
			return err
		}
		name := strings.TrimSpace(in.Name)
		var v ValidationError
		v.Problems = append(v.Problems, robotNameProblems(name)...)
		if in.OwnerUserID != "" && !IsUUID(in.OwnerUserID) {
			v.Problems = append(v.Problems, FieldProblem{Field: "owner_user_id", Code: CodeInvalid})
		}
		if len(v.Problems) > 0 {
			return &v
		}
		if in.OwnerUserID != "" {
			if err := checkRobotOwner(ctx, tx, in.OwnerUserID); err != nil {
				return err
			}
		}
		r, err := tx.Robots().InsertRobot(ctx, name, in.OwnerUserID)
		if err != nil {
			return robotWriteError("insert robot", err)
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: AuditRobotCreate, targetType: TargetRobot, targetID: r.ID, after: docOfRobot(r),
		}); err != nil {
			return err
		}
		out = r
		out.Detail = true
		return nil
	})
	return out, err
}

// UpdateRobot renames a robot, disables or enables it, or changes its owner (platform admins only; an area
// supervisor, browser session only, may change the owner and nothing else, see authorizeRobotUpdate). Disabling
// revokes every live token of the robot in the same transaction, through the repository function shared with the
// disable of a human, and the audit row lists the revoked ids; re-enabling brings nothing back. A patch that
// changes nothing writes no audit row and returns the robot as it is. A human id is ErrNotFound.
//
// The robot row is locked FOR NO KEY UPDATE (the lock token creation takes), so a disable and a creation for the
// same robot run one after the other. It is a leaf lock: no other explicit lock is held with it. The caller is
// authorized before the lock (one with no scope locks nothing) and again from the locked row, so two reassignments
// of one robot run one after the other and the second is checked against the owner the first one wrote.
func (s *Service) UpdateRobot(ctx context.Context, callerID, robotID string, p RobotPatch) (Robot, error) {
	robotID, ok := canonicalUUID(robotID)
	if !ok {
		return Robot{}, ErrNotFound
	}
	if p.OwnerUserID != nil {
		owner := lowerIfUUID(*p.OwnerUserID)
		p.OwnerUserID = &owner
	}
	var out Robot
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Robot{}
		if _, _, _, err := authorizeRobotUpdate(ctx, tx, callerID, robotID, p, nil); err != nil {
			return err
		}
		if err := validateRobotPatch(p); err != nil {
			return err
		}
		cur, err := tx.Robots().LockRobot(ctx, robotID)
		if err != nil {
			return err
		}
		sub, d, areaID, err := authorizeRobotUpdate(ctx, tx, callerID, robotID, p, &cur)
		if err != nil {
			return err
		}
		if d.Reason == adminz.ReasonAreaSupervisor && *p.OwnerUserID == "" {
			return invalid("owner_user_id", CodeNotAllowed) // it would drop the robot out of every supervisor's scope
		}
		next := cur
		if p.Name != nil {
			next.Name = strings.TrimSpace(*p.Name)
		}
		if p.Status != nil {
			next.Status = *p.Status
		}
		if p.OwnerUserID != nil {
			next.OwnerUserID = *p.OwnerUserID
		}
		next.Detail = true
		if next.Name == cur.Name && next.Status == cur.Status && next.OwnerUserID == cur.OwnerUserID {
			out = cur
			out.Detail = true
			return fillLastUsed(ctx, tx, &out)
		}
		if next.OwnerUserID != "" && next.OwnerUserID != cur.OwnerUserID {
			if err := checkNewOwner(ctx, tx, sub, d, next.OwnerUserID); err != nil {
				return err
			}
		}
		if err := tx.Robots().UpdateRobot(ctx, next); err != nil {
			return robotWriteError("update robot", err)
		}
		after := docOfRobot(next)
		if next.Status == UserStatusDisabled && cur.Status != UserStatusDisabled {
			if after.TokensRevoked, err = tx.Users().RevokeTokens(ctx, next.ID); err != nil {
				return fmt.Errorf("revoke robot tokens: %w", err)
			}
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: AuditRobotUpdate, targetType: TargetRobot, targetID: next.ID,
			areaID: supervisorArea(d, areaID), before: docOfRobot(cur), after: after,
		}); err != nil {
			return err
		}
		if out, err = tx.Robots().GetRobot(ctx, next.ID); err != nil { // re-read: the new owner's email and name
			return fmt.Errorf("reread robot: %w", err)
		}
		out.Detail = true
		return fillLastUsed(ctx, tx, &out)
	})
	return out, err
}

// authorizeRobotUpdate authorizes a robot patch. Platform admins pass robot.update. Anyone else needs robot.reassign
// and may send only owner_user_id (ErrForbidden otherwise); adminz sees the area of the robot's current owner, read
// from cur when the robot is already locked, otherwise from the database. It returns the subject, the decision and
// that area (the one an audit row records when the decision is area_supervisor).
func authorizeRobotUpdate(ctx context.Context, tx Tx, callerID, robotID string, p RobotPatch, cur *Robot) (adminz.Subject, adminz.Decision, string, error) {
	sub, err := subject(ctx, tx, callerID)
	if err != nil {
		return sub, adminz.Decision{}, "", err
	}
	res := adminz.Resource{Type: TargetRobot, ID: robotID}
	if sub.PlatformAdmin {
		d, err := authorize(sub, adminz.RobotUpdate, res)
		return sub, d, "", err
	}
	if p.Name != nil || p.Status != nil || p.OwnerUserID == nil {
		return sub, adminz.Decision{}, "", ErrForbidden
	}
	if cur == nil {
		r, err := tx.Robots().GetRobot(ctx, robotID)
		if err != nil {
			return sub, adminz.Decision{}, "", err
		}
		cur = &r
	}
	if res.AreaID, err = personArea(ctx, tx, cur.OwnerUserID); err != nil {
		return sub, adminz.Decision{}, "", err
	}
	d, err := authorize(sub, adminz.RobotReassign, res)
	return sub, d, res.AreaID, err
}

// checkNewOwner validates the owner a patch sets. A supervisor's new owner must also be supervisable and in an area
// the supervisor supervises (any of them); all the failures are the same 422 as for an unknown owner.
func checkNewOwner(ctx context.Context, tx Tx, sub adminz.Subject, d adminz.Decision, ownerID string) error {
	if err := checkRobotOwner(ctx, tx, ownerID); err != nil {
		return err
	}
	if d.Reason != adminz.ReasonAreaSupervisor {
		return nil
	}
	area, err := personArea(ctx, tx, ownerID)
	if err != nil {
		return err
	}
	if area == "" || !sub.Supervises(area) {
		return invalid("owner_user_id", CodeUnknown)
	}
	return nil
}

func fillLastUsed(ctx context.Context, tx Tx, r *Robot) error {
	var err error
	if r.LastUsedAt, err = tx.Robots().LastUsed(ctx, r.ID); err != nil {
		return fmt.Errorf("robot last use: %w", err)
	}
	return nil
}

func validateRobotPatch(p RobotPatch) error {
	var v ValidationError
	if p.Name == nil && p.Status == nil && p.OwnerUserID == nil {
		v.Problems = append(v.Problems, FieldProblem{Field: "name", Code: CodeAtLeastOne},
			FieldProblem{Field: "status", Code: CodeAtLeastOne}, FieldProblem{Field: "owner_user_id", Code: CodeAtLeastOne})
	}
	if p.Name != nil {
		v.Problems = append(v.Problems, robotNameProblems(strings.TrimSpace(*p.Name))...)
	}
	if p.Status != nil {
		switch *p.Status {
		case UserStatusActive, UserStatusDisabled:
		case "":
			v.Problems = append(v.Problems, FieldProblem{Field: "status", Code: CodeRequired})
		default:
			v.Problems = append(v.Problems, FieldProblem{Field: "status", Code: CodeInvalid})
		}
	}
	if p.OwnerUserID != nil && *p.OwnerUserID != "" && !IsUUID(*p.OwnerUserID) {
		v.Problems = append(v.Problems, FieldProblem{Field: "owner_user_id", Code: CodeInvalid})
	}
	if len(v.Problems) > 0 {
		return &v
	}
	return nil
}

// CreateRobotToken creates a token owned by a robot (platform admins only) and returns it with its secret. Like a
// personal token it needs a browser session (a request authenticated by a token is ErrForbidden) and counts
// against MaxLiveTokens, but its lifetime cap is PORTAL_TOKEN_MAX_TTL_ROBOT and the creator recorded in
// created_by is the admin. The scope admin needs the robot to hold a manages_access grant right now (a robot is
// never a platform admin): a token never carries more than its owner can do at issuance.
//
// Same unit of work as CreateOwnToken: the robot row is locked first (LockRobot, the robot counterpart of
// LockUser), its status is read again under the lock (a disabled robot is ErrRobotDisabled), then the live tokens
// are counted and the token inserted. Without the lock a token could be inserted after a concurrent disable
// revoked the others, and parallel creations could pass the count together.
func (s *Service) CreateRobotToken(ctx context.Context, callerID, robotID string, in NewToken) (CreatedToken, error) {
	if TokenFrom(ctx) != "" {
		return CreatedToken{}, ErrForbidden
	}
	robotID, ok := canonicalUUID(robotID)
	if !ok {
		return CreatedToken{}, ErrNotFound
	}
	res := adminz.Resource{Type: TargetToken}
	var out CreatedToken
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = CreatedToken{}
		if _, _, err := begin(ctx, tx, callerID, adminz.RobotTokenCreate, res); err != nil { // before locking: a non-admin locks nothing
			return err
		}
		r, err := tx.Robots().LockRobot(ctx, robotID)
		if err != nil {
			return err
		}
		if _, _, err := begin(ctx, tx, callerID, adminz.RobotTokenCreate, res); err != nil {
			return err
		}
		if r.Status != UserStatusActive {
			return ErrRobotDisabled
		}
		name, ttl, err := validateNewToken(in, s.tokenMaxTTLRobot)
		if err != nil {
			return err
		}
		if in.Scope == TokenScopeAdmin {
			p, err := tx.Permissions().LoadPermissions(ctx, r.ID)
			if err != nil {
				return fmt.Errorf("load robot permissions: %w", err)
			}
			if !p.PlatformAdmin && len(p.Managed) == 0 {
				return invalid("scope", CodeNotAllowed)
			}
		}
		tok, err := issueToken(ctx, tx, r.ID, callerID, name, in.Scope, ttl)
		if err != nil {
			return err
		}
		out = tok
		return nil
	})
	return out, err
}

// ListRobotTokens lists a robot's tokens, newest first, including the recently dead ones: platform admins for any
// robot, a supervisor (browser session only) for the robots owned by a supervisable person of their areas.
func (s *Service) ListRobotTokens(ctx context.Context, callerID, robotID string, req PageRequest) (Page[APIToken], error) {
	robotID, ok := canonicalUUID(robotID)
	if !ok {
		return Page[APIToken]{}, ErrNotFound
	}
	size := pageSize(req.Limit)
	var out Page[APIToken]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[APIToken]{}
		sub, err := subject(ctx, tx, callerID)
		if err != nil {
			return err
		}
		r, err := tx.Robots().GetRobot(ctx, robotID)
		if err != nil {
			return err
		}
		res := adminz.Resource{Type: TargetToken}
		if !sub.PlatformAdmin {
			if res.AreaID, err = personArea(ctx, tx, r.OwnerUserID); err != nil {
				return err
			}
		}
		if _, err := authorize(sub, adminz.RobotTokenList, res); err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindTokens, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Tokens().TokensOf(ctx, robotID, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list robot tokens: %w", err)
		}
		out = pageOf(rows, size, cursorKindTokens, func(t APIToken) Cursor { return Cursor{ID: t.ID} })
		return nil
	})
	return out, err
}

// ListUserRobots lists the robots a human owns, by id, with the fields platform admins see: platform admins for any
// human, a supervisor (browser session only) for the supervisable people of their areas.
func (s *Service) ListUserRobots(ctx context.Context, callerID, userID string, req PageRequest) (Page[Robot], error) {
	userID, ok := canonicalUUID(userID)
	if !ok {
		return Page[Robot]{}, ErrNotFound
	}
	size := pageSize(req.Limit)
	var out Page[Robot]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[Robot]{}
		if err := authorizeUserTarget(ctx, tx, callerID, userID, adminz.UserRobotList); err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindRobots, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Robots().ListRobotsOf(ctx, userID, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list user robots: %w", err)
		}
		for i := range rows {
			rows[i].Detail = true
		}
		out = pageOf(rows, size, cursorKindRobots, func(r Robot) Cursor { return Cursor{ID: r.ID} })
		return nil
	})
	return out, err
}
