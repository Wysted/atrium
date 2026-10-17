package portalhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"platform/api/internal/portal"
)

// AdminService is the use-case port of the administration endpoints (implemented by portal.Service). Every
// method takes the caller's user id first: authorization happens inside, in the unit of work of the mutation.
type AdminService interface {
	ListInitiatives(ctx context.Context, callerID string, req portal.PageRequest) (portal.Page[portal.Initiative], error)
	GetInitiative(ctx context.Context, callerID, initiativeID string) (portal.Initiative, error)
	CreateInitiative(ctx context.Context, callerID string, in portal.NewInitiative) (portal.Initiative, error)
	UpdateInitiative(ctx context.Context, callerID, initiativeID string, p portal.InitiativePatch) (portal.Initiative, error)

	ListRoles(ctx context.Context, callerID, initiativeID string, req portal.PageRequest) (portal.Page[portal.Role], error)
	CreateRole(ctx context.Context, callerID, initiativeID string, in portal.NewRole) (portal.Role, error)
	UpdateRole(ctx context.Context, callerID, roleID string, p portal.RolePatch) (portal.Role, error)
	DeleteRole(ctx context.Context, callerID, roleID string) error

	ListApps(ctx context.Context, callerID, initiativeID string, req portal.PageRequest) (portal.Page[portal.App], error)
	CreateApp(ctx context.Context, callerID, initiativeID string, in portal.NewApp) (portal.App, error)
	UpdateApp(ctx context.Context, callerID, appID string, p portal.AppPatch) (portal.App, error)
	CreateDeployment(ctx context.Context, callerID, appID string, in portal.NewDeployment) (portal.Deployment, error)
	DeleteDeployment(ctx context.Context, callerID, deploymentID string) error

	ListGrants(ctx context.Context, callerID, initiativeID string, req portal.PageRequest) (portal.Page[portal.Grant], error)
	CreateGrant(ctx context.Context, callerID, initiativeID string, in portal.NewGrant) (portal.Grant, error)
	DeleteGrant(ctx context.Context, callerID, grantID string) error

	ListGroups(ctx context.Context, callerID string, req portal.PageRequest, search string) (portal.Page[portal.Group], error)
	GetGroup(ctx context.Context, callerID, groupID string) (portal.Group, error)
	CreateGroup(ctx context.Context, callerID string, in portal.NewGroup) (portal.Group, error)
	DeleteGroup(ctx context.Context, callerID, groupID string) error
	ListGroupMembers(ctx context.Context, callerID, groupID string, req portal.PageRequest) (portal.Page[portal.GroupMember], error)
	AddGroupMember(ctx context.Context, callerID, groupID, email string) (portal.GroupMember, error)
	RemoveGroupMember(ctx context.Context, callerID, groupID, userID string) error

	FindUsers(ctx context.Context, callerID, email string) ([]portal.User, error)
	SetUserStatus(ctx context.Context, callerID, userID, status string) (portal.User, error)
	RevokeUserSessions(ctx context.Context, callerID, userID string) (int64, error)

	ListAudit(ctx context.Context, callerID string, q portal.AuditQuery) (portal.Page[portal.AuditEvent], error)

	AppHealth(ctx context.Context, callerID, appID string) (portal.AppHealth, error)
	ListOrphanHosts(ctx context.Context, callerID string) ([]portal.OrphanHost, error)
}

var _ AdminService = (*portal.Service)(nil)

// Routes of the administration API (contract block 6). Ids come from the URL, never keys.
const (
	PathInitiatives      = "/api/v1/initiatives"
	PathInitiative       = "/api/v1/initiatives/{id}"
	PathInitiativeRoles  = "/api/v1/initiatives/{id}/roles"
	PathRole             = "/api/v1/roles/{id}"
	PathInitiativeApps   = "/api/v1/initiatives/{id}/apps"
	PathApp              = "/api/v1/apps/{id}"
	PathAppDeployments   = "/api/v1/apps/{id}/deployments"
	PathDeployment       = "/api/v1/deployments/{id}"
	PathInitiativeGrants = "/api/v1/initiatives/{id}/grants"
	PathGrant            = "/api/v1/grants/{id}"
	PathGroups           = "/api/v1/groups"
	PathGroup            = "/api/v1/groups/{id}"
	PathGroupMembers     = "/api/v1/groups/{id}/members"
	PathGroupMember      = "/api/v1/groups/{id}/members/{user_id}"
	PathUsers            = "/api/v1/users"
	PathUser             = "/api/v1/users/{id}"
	PathUserSessions     = "/api/v1/users/{id}/sessions"
	PathAudit            = "/api/v1/audit"
	PathAppHealth        = "/api/v1/apps/{id}/health"
	PathOrphanHosts      = "/api/v1/health/orphan-hosts"
)

// Query parameters.
const (
	queryCursor       = "cursor"
	queryLimit        = "limit"
	queryText         = "q"
	queryEmail        = "email"
	queryInitiativeID = "initiative_id"
)

func (a *api) adminRoutes(mux *http.ServeMux) {
	a.catalogRoutes(mux)

	mux.HandleFunc("GET "+PathInitiativeGrants, a.listGrants)
	mux.HandleFunc("POST "+PathInitiativeGrants, a.createGrant)
	mux.HandleFunc("DELETE "+PathGrant, a.deleteGrant)

	mux.HandleFunc("GET "+PathGroups, a.listGroups)
	mux.HandleFunc("POST "+PathGroups, a.createGroup)
	mux.HandleFunc("GET "+PathGroup, a.getGroup)
	mux.HandleFunc("DELETE "+PathGroup, a.deleteGroup)
	mux.HandleFunc("GET "+PathGroupMembers, a.listMembers)
	mux.HandleFunc("POST "+PathGroupMembers, a.addMember)
	mux.HandleFunc("DELETE "+PathGroupMember, a.removeMember)

	mux.HandleFunc("GET "+PathUsers, a.findUsers)
	mux.HandleFunc("PATCH "+PathUser, a.patchUser)
	mux.HandleFunc("DELETE "+PathUserSessions, a.revokeSessions)

	mux.HandleFunc("GET "+PathAudit, a.listAudit)

	mux.HandleFunc("GET "+PathAppHealth, a.appHealth)
	mux.HandleFunc("GET "+PathOrphanHosts, a.orphanHosts)
}

// Request bodies. Decoding rejects unknown fields; the values are validated by the use cases.

type createGrantRequest struct {
	Email     string     `json:"email"`
	GroupID   string     `json:"group_id"`
	RoleID    string     `json:"role_id"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type createGroupRequest struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type addMemberRequest struct {
	Email string `json:"email"`
}

type patchUserRequest struct {
	Status string `json:"status"`
}

// Response shapes. Lists are never null; optional members are explicit nulls.

type roleJSON struct {
	ID            string `json:"id"`
	Key           string `json:"key"`
	ManagesAccess bool   `json:"manages_access"`
}

type userRefJSON struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type groupRefJSON struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type grantJSON struct {
	ID           string        `json:"id"`
	InitiativeID string        `json:"initiative_id"`
	Role         roleJSON      `json:"role"`
	User         *userRefJSON  `json:"user"`
	Group        *groupRefJSON `json:"group"`
	ExpiresAt    *time.Time    `json:"expires_at"`
	GrantedBy    *string       `json:"granted_by"`
	GrantedAt    time.Time     `json:"granted_at"`
}

type groupJSON struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type memberJSON struct {
	UserID  string    `json:"user_id"`
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	Status  string    `json:"status"`
	AddedAt time.Time `json:"added_at"`
}

type adminUserJSON struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type auditEventJSON struct {
	ID           string          `json:"id"`
	OccurredAt   time.Time       `json:"occurred_at"`
	ActorID      *string         `json:"actor_id"`
	ActorEmail   *string         `json:"actor_email"`
	InitiativeID *string         `json:"initiative_id"`
	Action       string          `json:"action"`
	TargetType   string          `json:"target_type"`
	TargetID     string          `json:"target_id"`
	Before       json.RawMessage `json:"before"`
	After        json.RawMessage `json:"after"`
}

// listResponse is {"<name>": [...], "next_cursor": "..."} with the list under a per-endpoint name.
func listResponse[T any](name string, items []T, next string) map[string]any {
	if items == nil {
		items = []T{}
	}
	out := map[string]any{name: items}
	if next != "" {
		out["next_cursor"] = next
	}
	return out
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func grantToJSON(g portal.Grant) grantJSON {
	out := grantJSON{
		ID: g.ID, InitiativeID: g.InitiativeID,
		Role:      roleJSON{ID: g.Role.ID, Key: g.Role.Key, ManagesAccess: g.Role.ManagesAccess},
		ExpiresAt: g.ExpiresAt, GrantedBy: nilIfEmpty(g.GrantedBy), GrantedAt: g.GrantedAt,
	}
	if g.User != nil {
		u := userRefJSON(*g.User)
		out.User = &u
	}
	if g.Group != nil {
		grp := groupRefJSON(*g.Group)
		out.Group = &grp
	}
	return out
}

func mapSlice[S, T any](in []S, f func(S) T) []T {
	out := make([]T, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

// pageRequest reads the cursor and limit query parameters. A limit that is not a positive integer is a
// validation problem; above portal.MaxPageSize it is capped by the use case. The cursor is passed on opaque.
func pageRequest(w http.ResponseWriter, r *http.Request) (portal.PageRequest, bool) {
	q := r.URL.Query()
	req := portal.PageRequest{Cursor: q.Get(queryCursor)}
	if raw := q.Get(queryLimit); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeValidation(w, []portal.FieldProblem{{Field: queryLimit, Code: portal.CodeInvalid}})
			return portal.PageRequest{}, false
		}
		req.Limit = n
	}
	return req, true
}

// Grants.

func (a *api) listGrants(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListGrants(r.Context(), uid, r.PathValue("id"), req)
	if err != nil {
		a.fail(w, r, "grants_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("grants", mapSlice(p.Items, grantToJSON), p.NextCursor))
}

func (a *api) createGrant(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createGrantRequest](w, r)
	if !ok {
		return
	}
	g, err := a.admin.CreateGrant(r.Context(), uid, r.PathValue("id"), portal.NewGrant(in))
	if err != nil {
		a.fail(w, r, "grant_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, grantToJSON(g))
}

func (a *api) deleteGrant(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	if err := a.admin.DeleteGrant(r.Context(), uid, r.PathValue("id")); err != nil {
		a.fail(w, r, "grant_delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Groups and members.

func (a *api) listGroups(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListGroups(r.Context(), uid, req, r.URL.Query().Get(queryText))
	if err != nil {
		a.fail(w, r, "groups_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("groups", mapSlice(p.Items, groupToJSON), p.NextCursor))
}

func (a *api) getGroup(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	g, err := a.admin.GetGroup(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, "group_get", err)
		return
	}
	WriteJSON(w, http.StatusOK, groupToJSON(g))
}

func groupToJSON(g portal.Group) groupJSON { return groupJSON(g) }

func (a *api) createGroup(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createGroupRequest](w, r)
	if !ok {
		return
	}
	g, err := a.admin.CreateGroup(r.Context(), uid, portal.NewGroup(in))
	if err != nil {
		a.fail(w, r, "group_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, groupToJSON(g))
}

func (a *api) deleteGroup(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	if err := a.admin.DeleteGroup(r.Context(), uid, r.PathValue("id")); err != nil {
		a.fail(w, r, "group_delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func memberToJSON(m portal.GroupMember) memberJSON { return memberJSON(m) }

func (a *api) listMembers(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListGroupMembers(r.Context(), uid, r.PathValue("id"), req)
	if err != nil {
		a.fail(w, r, "group_members_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("members", mapSlice(p.Items, memberToJSON), p.NextCursor))
}

func (a *api) addMember(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[addMemberRequest](w, r)
	if !ok {
		return
	}
	m, err := a.admin.AddGroupMember(r.Context(), uid, r.PathValue("id"), in.Email)
	if err != nil {
		a.fail(w, r, "group_member_add", err)
		return
	}
	WriteJSON(w, http.StatusCreated, memberToJSON(m))
}

func (a *api) removeMember(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	if err := a.admin.RemoveGroupMember(r.Context(), uid, r.PathValue("id"), r.PathValue("user_id")); err != nil {
		a.fail(w, r, "group_member_remove", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Users.

func userToJSON(u portal.User) adminUserJSON { return adminUserJSON(u) }

func (a *api) findUsers(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	users, err := a.admin.FindUsers(r.Context(), uid, r.URL.Query().Get(queryEmail))
	if err != nil {
		a.fail(w, r, "users_find", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("users", mapSlice(users, userToJSON), ""))
}

func (a *api) patchUser(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[patchUserRequest](w, r)
	if !ok {
		return
	}
	u, err := a.admin.SetUserStatus(r.Context(), uid, r.PathValue("id"), in.Status)
	if err != nil {
		a.fail(w, r, "user_patch", err)
		return
	}
	WriteJSON(w, http.StatusOK, userToJSON(u))
}

func (a *api) revokeSessions(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	n, err := a.admin.RevokeUserSessions(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, "user_sessions_revoke", err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}

// Audit.

func eventToJSON(e portal.AuditEvent) auditEventJSON {
	return auditEventJSON{
		ID: e.ID, OccurredAt: e.OccurredAt, ActorID: nilIfEmpty(e.ActorID), ActorEmail: nilIfEmpty(e.ActorEmail),
		InitiativeID: nilIfEmpty(e.InitiativeID), Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
		Before: rawOrNull(e.Before), After: rawOrNull(e.After),
	}
}

func rawOrNull(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

func (a *api) listAudit(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	q := portal.AuditQuery{InitiativeID: r.URL.Query().Get(queryInitiativeID), Page: req}
	p, err := a.admin.ListAudit(r.Context(), uid, q)
	if err != nil {
		a.fail(w, r, "audit_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("events", mapSlice(p.Items, eventToJSON), p.NextCursor))
}
