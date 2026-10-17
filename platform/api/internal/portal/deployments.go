package portal

import (
	"context"
	"errors"
	"fmt"

	"github.com/Wysted/atrium/platform/api/internal/adminz"
	"github.com/Wysted/atrium/platform/api/internal/hostname"
)

// deploymentDoc is the audit document of a deployment.
type deploymentDoc struct {
	AppID string `json:"app_id"`
	Host  string `json:"host"`
}

func deploymentResource(d Deployment) adminz.Resource {
	return adminz.Resource{Type: TargetDeployment, ID: d.ID, InitiativeID: d.InitiativeID}
}

// CreateDeployment attaches a host to an app (platform admins only). The host must be valid (see hostname.Valid)
// and must not be the auth host (CodeReserved). A host that already exists, on this app or on any other one
// (the portal's own hosts included), is ErrHostTaken and creates nothing.
func (s *Service) CreateDeployment(ctx context.Context, callerID, appID string, in NewDeployment) (Deployment, error) {
	if !appIDPattern.MatchString(appID) {
		return Deployment{}, ErrNotFound
	}
	var out Deployment
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Deployment{}
		sub, err := subject(ctx, tx, callerID)
		if err != nil {
			return err
		}
		a, err := tx.Catalog().GetApp(ctx, appID)
		if err != nil {
			return err
		}
		if _, err := authorize(sub, adminz.DeploymentCreate, adminz.Resource{Type: TargetDeployment, InitiativeID: a.InitiativeID}); err != nil {
			return err
		}
		switch {
		case in.Host == "":
			return invalid("host", CodeRequired)
		case !hostname.Valid(in.Host):
			return invalid("host", CodeInvalid)
		case s.authHost != "" && in.Host == s.authHost:
			return invalid("host", CodeReserved)
		}
		d, err := tx.Catalog().InsertDeployment(ctx, a.ID, in.Host)
		var ref *ReferenceError
		switch {
		case errors.Is(err, ErrDuplicate):
			return ErrHostTaken
		case errors.As(err, &ref): // the app vanished meanwhile
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("insert deployment: %w", err)
		}
		d.InitiativeID = a.InitiativeID
		if err := record(ctx, tx, event{
			actor: callerID, action: adminz.DeploymentCreate, targetType: TargetDeployment, targetID: d.ID,
			initiativeID: a.InitiativeID, after: deploymentDoc{AppID: d.AppID, Host: d.Host},
		}); err != nil {
			return err
		}
		out = d
		return nil
	})
	return out, err
}

// DeleteDeployment removes a host from its app (platform admins only). The last deployment of the portal app
// cannot be removed (ErrLastPortalDeployment): without a host nobody could reach the portal to repair it.
//
// The app row is locked FOR NO KEY UPDATE before the deployments are counted, and the deployment is read again
// after the lock. Two removals of the last two deployments therefore run one after the other and the second
// counts what the first left; without the lock both would count two and both delete. The app row is a leaf of
// the lock order of the portal: nothing else is locked while holding it. Repeating a removal is ErrNotFound.
func (s *Service) DeleteDeployment(ctx context.Context, callerID, deploymentID string) error {
	deploymentID, ok := canonicalUUID(deploymentID)
	if !ok {
		return ErrNotFound
	}
	return s.store.WithTx(ctx, func(tx Tx) error {
		sub, err := subject(ctx, tx, callerID)
		if err != nil {
			return err
		}
		d, err := tx.Catalog().GetDeployment(ctx, deploymentID)
		if err != nil {
			return err
		}
		if _, err := authorize(sub, adminz.DeploymentDelete, deploymentResource(d)); err != nil {
			return err
		}

		a, err := tx.Catalog().LockApp(ctx, d.AppID)
		if err != nil {
			return err
		}
		if d, err = tx.Catalog().GetDeployment(ctx, deploymentID); err != nil {
			return err
		}
		if s.isPortalApp(a) {
			n, err := tx.Catalog().CountDeployments(ctx, a.ID)
			if err != nil {
				return fmt.Errorf("count deployments: %w", err)
			}
			if n <= 1 {
				return ErrLastPortalDeployment
			}
		}
		deleted, err := tx.Catalog().DeleteDeployment(ctx, d.ID)
		if err != nil {
			return fmt.Errorf("delete deployment: %w", err)
		}
		if !deleted {
			return ErrNotFound
		}
		return record(ctx, tx, event{
			actor: callerID, action: adminz.DeploymentDelete, targetType: TargetDeployment, targetID: d.ID,
			initiativeID: d.InitiativeID, before: deploymentDoc{AppID: d.AppID, Host: d.Host},
		})
	})
}
