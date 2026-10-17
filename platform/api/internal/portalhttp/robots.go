package portalhttp

import (
	"net/http"
	"time"

	"platform/api/internal/portal"
)

// Routes of the robots (contract block 6, "Non-browser clients").
const (
	PathRobots      = "/api/v1/robots"
	PathRobot       = "/api/v1/robots/{id}"
	PathRobotTokens = "/api/v1/robots/{id}/tokens" //nolint:gosec // G101: a URL path, not a credential
)

func (a *api) robotRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+PathRobots, a.listRobots)
	mux.HandleFunc("POST "+PathRobots, a.createRobot)
	mux.HandleFunc("PATCH "+PathRobot, a.patchRobot)
	mux.HandleFunc("GET "+PathRobotTokens, a.listRobotTokens)
	mux.HandleFunc("POST "+PathRobotTokens, a.createRobotToken)
}

type createRobotRequest struct {
	Name        string `json:"name"`
	OwnerUserID string `json:"owner_user_id"`
}

// patchRobotRequest: an owner_user_id of "" clears the owner (a JSON null cannot be told from an absent field).
type patchRobotRequest struct {
	Name        *string `json:"name"`
	Status      *string `json:"status"`
	OwnerUserID *string `json:"owner_user_id"`
}

// robotJSON is a robot as platform admins see it.
type robotJSON struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	OwnerUserID *string    `json:"owner_user_id"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

// robotRefJSON is what a manager sees in the listing: enough to grant.
type robotRefJSON struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func robotToJSON(r portal.Robot) any {
	if !r.Detail {
		return robotRefJSON{ID: r.ID, Name: r.Name, Status: r.Status}
	}
	return robotJSON{
		ID: r.ID, Name: r.Name, Status: r.Status, OwnerUserID: nilIfEmpty(r.OwnerUserID), CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt,
	}
}

func (a *api) listRobots(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListRobots(r.Context(), uid, req)
	if err != nil {
		a.fail(w, r, "robots_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("robots", mapSlice(p.Items, robotToJSON), p.NextCursor))
}

func (a *api) createRobot(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createRobotRequest](w, r)
	if !ok {
		return
	}
	rb, err := a.admin.CreateRobot(r.Context(), uid, portal.NewRobot(in))
	if err != nil {
		a.fail(w, r, "robot_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, robotToJSON(rb))
}

func (a *api) patchRobot(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[patchRobotRequest](w, r)
	if !ok {
		return
	}
	rb, err := a.admin.UpdateRobot(r.Context(), uid, r.PathValue("id"), portal.RobotPatch(in))
	if err != nil {
		a.fail(w, r, "robot_patch", err)
		return
	}
	WriteJSON(w, http.StatusOK, robotToJSON(rb))
}

func (a *api) listRobotTokens(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListRobotTokens(r.Context(), uid, r.PathValue("id"), req)
	if err != nil {
		a.fail(w, r, "robot_tokens_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("tokens", mapSlice(p.Items, tokenToJSON), p.NextCursor))
}

func (a *api) createRobotToken(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createTokenRequest](w, r)
	if !ok {
		return
	}
	t, err := a.admin.CreateRobotToken(r.Context(), uid, r.PathValue("id"), newTokenFrom(in))
	if err != nil {
		a.fail(w, r, "robot_token_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, createdTokenJSON{tokenJSON: tokenToJSON(t.APIToken), Secret: t.Secret})
}
