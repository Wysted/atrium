package portalhttp

import (
	"context"
	"net/http"
	"time"

	"platform/api/internal/portal"
)

// Routes of the areas (contract block 6, "Area supervisors").
const (
	PathAreas           = "/api/v1/areas"
	PathArea            = "/api/v1/areas/{id}"
	PathAreaMembers     = "/api/v1/areas/{id}/members"
	PathAreaMember      = "/api/v1/areas/{id}/members/{user_id}"
	PathAreaSupervisors = "/api/v1/areas/{id}/supervisors"
	PathAreaSupervisor  = "/api/v1/areas/{id}/supervisors/{user_id}"
)

func (a *api) areaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+PathAreas, a.listAreas)
	mux.HandleFunc("POST "+PathAreas, a.createArea)
	mux.HandleFunc("GET "+PathArea, a.getArea)
	mux.HandleFunc("DELETE "+PathArea, a.deleteArea)
	mux.HandleFunc("GET "+PathAreaMembers, a.listAreaPeople("area_members_list", "members", a.admin.ListAreaMembers))
	mux.HandleFunc("POST "+PathAreaMembers, a.addAreaPerson("area_member_add", a.admin.AddAreaMember))
	mux.HandleFunc("DELETE "+PathAreaMember, a.removeAreaPerson("area_member_remove", a.admin.RemoveAreaMember))
	mux.HandleFunc("GET "+PathAreaSupervisors, a.listAreaPeople("area_supervisors_list", "supervisors", a.admin.ListAreaSupervisors))
	mux.HandleFunc("POST "+PathAreaSupervisors, a.addAreaPerson("area_supervisor_add", a.admin.AddAreaSupervisor))
	mux.HandleFunc("DELETE "+PathAreaSupervisor, a.removeAreaPerson("area_supervisor_remove", a.admin.RemoveAreaSupervisor))
}

type createAreaRequest struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type areaJSON struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// areaRefJSON is an area in /me.
type areaRefJSON struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

func areaToJSON(a portal.Area) areaJSON { return areaJSON(a) }

func areaPersonToJSON(p portal.AreaPerson) memberJSON { return memberJSON(p) }

func (a *api) listAreas(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListAreas(r.Context(), uid, req)
	if err != nil {
		a.fail(w, r, "areas_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("areas", mapSlice(p.Items, areaToJSON), p.NextCursor))
}

func (a *api) getArea(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	area, err := a.admin.GetArea(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, "area_get", err)
		return
	}
	WriteJSON(w, http.StatusOK, areaToJSON(area))
}

func (a *api) createArea(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createAreaRequest](w, r)
	if !ok {
		return
	}
	area, err := a.admin.CreateArea(r.Context(), uid, portal.NewArea(in))
	if err != nil {
		a.fail(w, r, "area_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, areaToJSON(area))
}

func (a *api) deleteArea(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	if err := a.admin.DeleteArea(r.Context(), uid, r.PathValue("id")); err != nil {
		a.fail(w, r, "area_delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listAreaPeople serves the listing of members or supervisors; key is the name of the list in the response.
func (a *api) listAreaPeople(op, key string, list func(context.Context, string, string, portal.PageRequest) (portal.Page[portal.AreaPerson], error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := a.caller(w, r)
		if !ok {
			return
		}
		req, ok := pageRequest(w, r)
		if !ok {
			return
		}
		p, err := list(r.Context(), uid, r.PathValue("id"), req)
		if err != nil {
			a.fail(w, r, op, err)
			return
		}
		WriteJSON(w, http.StatusOK, listResponse(key, mapSlice(p.Items, areaPersonToJSON), p.NextCursor))
	}
}

func (a *api) addAreaPerson(op string, add func(ctx context.Context, callerID, areaID, email string) (portal.AreaPerson, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := a.caller(w, r)
		if !ok {
			return
		}
		in, ok := DecodeJSON[addMemberRequest](w, r)
		if !ok {
			return
		}
		p, err := add(r.Context(), uid, r.PathValue("id"), in.Email)
		if err != nil {
			a.fail(w, r, op, err)
			return
		}
		WriteJSON(w, http.StatusCreated, areaPersonToJSON(p))
	}
}

func (a *api) removeAreaPerson(op string, remove func(context.Context, string, string, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := a.caller(w, r)
		if !ok {
			return
		}
		if err := remove(r.Context(), uid, r.PathValue("id"), r.PathValue("user_id")); err != nil {
			a.fail(w, r, op, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
