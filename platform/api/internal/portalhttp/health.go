package portalhttp

import (
	"net/http"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/portal"
)

type problemJSON struct {
	Code string  `json:"code"`
	Host *string `json:"host"`
}

type appHealthJSON struct {
	App                    appJSON       `json:"app"`
	InitiativeStatus       string        `json:"initiative_status"`
	GrantCount             int           `json:"grant_count"`
	EffectiveManagerGrants int           `json:"effective_manager_grants"`
	Problems               []problemJSON `json:"problems"`
	CheckedAt              time.Time     `json:"checked_at"`
}

type orphanHostJSON struct {
	Host         string    `json:"host"`
	Count        int64     `json:"count"`
	LastDeniedAt time.Time `json:"last_denied_at"`
}

func healthToJSON(h portal.AppHealth) appHealthJSON {
	return appHealthJSON{
		App:                    appToJSON(h.App),
		InitiativeStatus:       h.InitiativeStatus,
		GrantCount:             h.Grants.Grants,
		EffectiveManagerGrants: h.Grants.EffectiveManagers,
		Problems: mapSlice(h.Problems, func(p portal.HealthProblem) problemJSON {
			return problemJSON{Code: p.Code, Host: nilIfEmpty(p.Host)}
		}),
		CheckedAt: h.CheckedAt,
	}
}

func (a *api) appHealth(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	h, err := a.admin.AppHealth(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, "app_health", err)
		return
	}
	WriteJSON(w, http.StatusOK, healthToJSON(h))
}

func (a *api) orphanHosts(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	hosts, err := a.admin.ListOrphanHosts(r.Context(), uid)
	if err != nil {
		a.fail(w, r, "orphan_hosts", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("hosts", mapSlice(hosts, func(h portal.OrphanHost) orphanHostJSON {
		return orphanHostJSON{Host: h.Host, Count: h.Count, LastDeniedAt: h.LastDeniedAt}
	}), ""))
}
