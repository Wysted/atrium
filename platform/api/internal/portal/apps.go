package portal

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"platform/api/internal/adminz"
)

// appIDPattern mirrors the apps.id CHECK.
var appIDPattern = regexp.MustCompile(`^app_[0-9A-HJKMNP-TV-Z]{26}$`)

// appDoc is the audit document of an app.
type appDoc struct {
	InitiativeID string `json:"initiative_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
}

func docOfApp(a App) appDoc {
	return appDoc{InitiativeID: a.InitiativeID, Name: a.Name, Status: a.Status}
}

func appResource(a App) adminz.Resource {
	return adminz.Resource{Type: TargetApp, ID: a.ID, InitiativeID: a.InitiativeID}
}

// isPortalApp reports whether a is the portal app, by the rule of the ForwardAuth evaluator
// (access.Evaluator): the configured app id AND the reserved initiative, so a mistyped PORTAL_APP_ID that
// points to another initiative's app protects nothing.
func (s *Service) isPortalApp(a App) bool {
	return s.portalAppID != "" && a.ID == s.portalAppID && a.InitiativeKey == PlatformInitiativeKey
}

// ListApps lists the apps of an initiative with their deployments (platform admins and the managers of it),
// oldest first.
func (s *Service) ListApps(ctx context.Context, callerID, initiativeID string, req PageRequest) (Page[App], error) {
	initiativeID, ok := canonicalUUID(initiativeID)
	if !ok {
		return Page[App]{}, ErrNotFound
	}
	size := pageSize(req.Limit)
	var out Page[App]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[App]{}
		_, d, err := begin(ctx, tx, callerID, adminz.AppList, adminz.Resource{Type: TargetApp, InitiativeID: initiativeID})
		if err != nil {
			return err
		}
		if _, err := tx.Catalog().GetInitiative(ctx, initiativeID); err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindApps, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Catalog().ListApps(ctx, d.Scope, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list apps: %w", err)
		}
		out = pageOf(rows, size, cursorKindApps, func(a App) Cursor { return Cursor{ID: a.ID} })
		return nil
	})
	return out, err
}

func validAppName(name string) bool { return len(name) <= maxAppName && cleanText(name) }

// CreateApp creates an app in an initiative (platform admins only) with a generated id, which is the `aud` of
// its assertions. The initiative is immutable afterwards. Apps have no natural key, so a repeated request
// creates a second app.
func (s *Service) CreateApp(ctx context.Context, callerID, initiativeID string, in NewApp) (App, error) {
	initiativeID, ok := canonicalUUID(initiativeID)
	if !ok {
		return App{}, ErrNotFound
	}
	var out App
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = App{}
		if _, _, err := begin(ctx, tx, callerID, adminz.AppCreate, adminz.Resource{Type: TargetApp, InitiativeID: initiativeID}); err != nil {
			return err
		}
		if _, err := tx.Catalog().GetInitiative(ctx, initiativeID); err != nil {
			return err
		}
		if !validAppName(in.Name) {
			return invalid("name", CodeInvalid)
		}
		id, err := generateAppID(s.now())
		if err != nil {
			return fmt.Errorf("generate app id: %w", err)
		}
		var ref *ReferenceError
		switch err := tx.Catalog().InsertApp(ctx, App{ID: id, InitiativeID: initiativeID, Name: in.Name, Status: AppStatusActive}); {
		case errors.As(err, &ref): // the initiative vanished meanwhile
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("insert app: %w", err)
		}
		a, err := tx.Catalog().GetApp(ctx, id)
		if err != nil {
			return fmt.Errorf("read created app: %w", err)
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.AppCreate, targetType: TargetApp, targetID: a.ID,
			initiativeID: initiativeID, after: docOfApp(a),
		}); err != nil {
			return err
		}
		out = a
		return nil
	})
	return out, err
}

// appAction is the action a patch asks for: a status change is an enable or a disable, anything else an update.
func appAction(p AppPatch) adminz.Action {
	if p.Status != nil {
		switch *p.Status {
		case AppStatusDisabled:
			return adminz.AppDisable
		case AppStatusActive:
			return adminz.AppEnable
		}
	}
	return adminz.AppUpdate
}

func validateAppPatch(p AppPatch) error {
	var v ValidationError
	if p.Name == nil && p.Status == nil {
		v.Problems = append(v.Problems, FieldProblem{Field: "name", Code: CodeAtLeastOne}, FieldProblem{Field: "status", Code: CodeAtLeastOne})
	}
	if p.Name != nil && !validAppName(*p.Name) {
		v.Problems = append(v.Problems, FieldProblem{Field: "name", Code: CodeInvalid})
	}
	if p.Status != nil {
		switch *p.Status {
		case AppStatusActive, AppStatusDisabled:
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

// UpdateApp renames an app and/or enables or disables it (platform admins only); the initiative is immutable.
// The portal app cannot be disabled (ErrPortalApp): it is the door to this administration. A patch that changes
// nothing writes no audit row and returns the app as it is. The app row is locked FOR NO KEY UPDATE before it
// is read again, the same lock deployment removal takes, so the two serialize per app.
func (s *Service) UpdateApp(ctx context.Context, callerID, appID string, p AppPatch) (App, error) {
	if !appIDPattern.MatchString(appID) {
		return App{}, ErrNotFound
	}
	var out App
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = App{}
		sub, err := subject(ctx, tx, callerID)
		if err != nil {
			return err
		}
		a, err := tx.Catalog().GetApp(ctx, appID)
		if err != nil {
			return err
		}
		if _, err := authorize(sub, appAction(p), appResource(a)); err != nil {
			return err
		}
		if err := validateAppPatch(p); err != nil {
			return err
		}
		if a, err = tx.Catalog().LockApp(ctx, appID); err != nil {
			return err
		}
		if p.Status != nil && *p.Status == AppStatusDisabled && s.isPortalApp(a) {
			return ErrPortalApp
		}
		next := a
		if p.Name != nil {
			next.Name = *p.Name
		}
		if p.Status != nil {
			next.Status = *p.Status
		}
		if next.Name == a.Name && next.Status == a.Status {
			out = a
			return nil
		}
		if err := tx.Catalog().UpdateApp(ctx, a.ID, next.Name, next.Status); err != nil {
			return fmt.Errorf("update app: %w", err)
		}
		action := adminz.AppUpdate
		if next.Status != a.Status {
			action = appAction(AppPatch{Status: &next.Status})
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: action, targetType: TargetApp, targetID: a.ID,
			initiativeID: a.InitiativeID, before: docOfApp(a), after: docOfApp(next),
		}); err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}
