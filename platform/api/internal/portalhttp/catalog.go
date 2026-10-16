package portalhttp

import (
	"net/http"
	"time"

	"platform/api/internal/portal"
)

func (a *api) catalogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+PathInitiatives, a.listInitiatives)
	mux.HandleFunc("POST "+PathInitiatives, a.createInitiative)
	mux.HandleFunc("GET "+PathInitiative, a.getInitiative)
	mux.HandleFunc("PATCH "+PathInitiative, a.patchInitiative)

	mux.HandleFunc("GET "+PathInitiativeRoles, a.listRoles)
	mux.HandleFunc("POST "+PathInitiativeRoles, a.createRole)
	mux.HandleFunc("PATCH "+PathRole, a.patchRole)
	mux.HandleFunc("DELETE "+PathRole, a.deleteRole)

	mux.HandleFunc("GET "+PathInitiativeApps, a.listApps)
	mux.HandleFunc("POST "+PathInitiativeApps, a.createApp)
	mux.HandleFunc("PATCH "+PathApp, a.patchApp)
	mux.HandleFunc("POST "+PathAppDeployments, a.createDeployment)
	mux.HandleFunc("DELETE "+PathDeployment, a.deleteDeployment)
}

// Request bodies. Keys and the initiative of an app are immutable: they are not part of any patch, so sending
// them is an unknown field (400).

type createInitiativeRequest struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type patchInitiativeRequest struct {
	Name   *string `json:"name"`
	Status *string `json:"status"`
}

type createRoleRequest struct {
	Key           string `json:"key"`
	ManagesAccess bool   `json:"manages_access"`
}

type patchRoleRequest struct {
	ManagesAccess *bool `json:"manages_access"`
}

type createAppRequest struct {
	Name string `json:"name"`
}

type patchAppRequest struct {
	Name   *string `json:"name"`
	Status *string `json:"status"`
}

type createDeploymentRequest struct {
	Host string `json:"host"`
}

// Response shapes.

type initiativeDetailJSON struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type roleDetailJSON struct {
	ID            string `json:"id"`
	InitiativeID  string `json:"initiative_id"`
	Key           string `json:"key"`
	ManagesAccess bool   `json:"manages_access"`
}

// deploymentJSON carries the integration telemetry of the deployment: null means never (seen or probed).
type deploymentJSON struct {
	ID              string     `json:"id"`
	AppID           string     `json:"app_id"`
	Host            string     `json:"host"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	LastProbeAt     *time.Time `json:"last_probe_at"`
	LastProbeStatus *string    `json:"last_probe_status"`
}

type appJSON struct {
	ID           string           `json:"id"`
	InitiativeID string           `json:"initiative_id"`
	Name         string           `json:"name"`
	Status       string           `json:"status"`
	CreatedAt    time.Time        `json:"created_at"`
	Deployments  []deploymentJSON `json:"deployments"`
}

func initiativeToJSON(i portal.Initiative) initiativeDetailJSON {
	return initiativeDetailJSON{ID: i.ID, Key: i.Key, Name: i.Name, Status: i.Status, CreatedAt: i.CreatedAt}
}

func roleToJSON(r portal.Role) roleDetailJSON {
	return roleDetailJSON{ID: r.ID, InitiativeID: r.InitiativeID, Key: r.Key, ManagesAccess: r.ManagesAccess}
}

func deploymentToJSON(d portal.Deployment) deploymentJSON {
	return deploymentJSON{
		ID: d.ID, AppID: d.AppID, Host: d.Host, CreatedAt: d.CreatedAt,
		LastSeenAt: d.LastSeenAt, LastProbeAt: d.LastProbeAt, LastProbeStatus: nilIfEmpty(d.LastProbeStatus),
	}
}

func appToJSON(a portal.App) appJSON {
	return appJSON{
		ID: a.ID, InitiativeID: a.InitiativeID, Name: a.Name, Status: a.Status, CreatedAt: a.CreatedAt,
		Deployments: mapSlice(a.Deployments, deploymentToJSON),
	}
}

// Initiatives.

func (a *api) listInitiatives(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListInitiatives(r.Context(), uid, req)
	if err != nil {
		a.fail(w, r, "initiatives_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("initiatives", mapSlice(p.Items, initiativeToJSON), p.NextCursor))
}

func (a *api) getInitiative(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	i, err := a.admin.GetInitiative(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, "initiative_get", err)
		return
	}
	WriteJSON(w, http.StatusOK, initiativeToJSON(i))
}

func (a *api) createInitiative(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createInitiativeRequest](w, r)
	if !ok {
		return
	}
	i, err := a.admin.CreateInitiative(r.Context(), uid, portal.NewInitiative(in))
	if err != nil {
		a.fail(w, r, "initiative_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, initiativeToJSON(i))
}

func (a *api) patchInitiative(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[patchInitiativeRequest](w, r)
	if !ok {
		return
	}
	i, err := a.admin.UpdateInitiative(r.Context(), uid, r.PathValue("id"), portal.InitiativePatch(in))
	if err != nil {
		a.fail(w, r, "initiative_patch", err)
		return
	}
	WriteJSON(w, http.StatusOK, initiativeToJSON(i))
}

// Roles.

func (a *api) listRoles(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListRoles(r.Context(), uid, r.PathValue("id"), req)
	if err != nil {
		a.fail(w, r, "roles_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("roles", mapSlice(p.Items, roleToJSON), p.NextCursor))
}

func (a *api) createRole(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createRoleRequest](w, r)
	if !ok {
		return
	}
	role, err := a.admin.CreateRole(r.Context(), uid, r.PathValue("id"), portal.NewRole(in))
	if err != nil {
		a.fail(w, r, "role_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, roleToJSON(role))
}

func (a *api) patchRole(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[patchRoleRequest](w, r)
	if !ok {
		return
	}
	role, err := a.admin.UpdateRole(r.Context(), uid, r.PathValue("id"), portal.RolePatch(in))
	if err != nil {
		a.fail(w, r, "role_patch", err)
		return
	}
	WriteJSON(w, http.StatusOK, roleToJSON(role))
}

func (a *api) deleteRole(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	if err := a.admin.DeleteRole(r.Context(), uid, r.PathValue("id")); err != nil {
		a.fail(w, r, "role_delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Apps and deployments.

func (a *api) listApps(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListApps(r.Context(), uid, r.PathValue("id"), req)
	if err != nil {
		a.fail(w, r, "apps_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("apps", mapSlice(p.Items, appToJSON), p.NextCursor))
}

func (a *api) createApp(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createAppRequest](w, r)
	if !ok {
		return
	}
	app, err := a.admin.CreateApp(r.Context(), uid, r.PathValue("id"), portal.NewApp(in))
	if err != nil {
		a.fail(w, r, "app_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, appToJSON(app))
}

func (a *api) patchApp(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[patchAppRequest](w, r)
	if !ok {
		return
	}
	app, err := a.admin.UpdateApp(r.Context(), uid, r.PathValue("id"), portal.AppPatch(in))
	if err != nil {
		a.fail(w, r, "app_patch", err)
		return
	}
	WriteJSON(w, http.StatusOK, appToJSON(app))
}

func (a *api) createDeployment(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createDeploymentRequest](w, r)
	if !ok {
		return
	}
	d, err := a.admin.CreateDeployment(r.Context(), uid, r.PathValue("id"), portal.NewDeployment(in))
	if err != nil {
		a.fail(w, r, "deployment_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, deploymentToJSON(d))
}

func (a *api) deleteDeployment(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	if err := a.admin.DeleteDeployment(r.Context(), uid, r.PathValue("id")); err != nil {
		a.fail(w, r, "deployment_delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
