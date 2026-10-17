# Observability

Everything critical works inside the portal, with no external tool; standard outputs feed whatever the adopter runs. The portal is a product adopted by many companies, so nothing here assumes a specific observability stack.

| Layer | Provided |
|---|---|
| Audit | `audit_events`, browsable in the portal and scoped by initiative for managers |
| Integration health (per app) | Deployments never or no longer seen (`last_seen_at`); result of the last probe; apps without grants or without a manager; disabled apps and inactive initiatives |
| Integration health (platform) | Orphan hosts: `unknown_host` denials of a host routed by Traefik with no deployment |
| Logs | JSON on stdout (already the case), for any collector |
| Metrics | `/metrics` in Prometheus format on a dedicated metrics listener of each process: decisions by reason, logins by outcome, JWKS fetches, key state, probe results ([catalog](#metrics)) |
| Dashboards and alerts | Optional Grafana dashboards and alert rules versioned in `platform-gitops/`; the local overlay adds Prometheus and Grafana |

## Metrics

Both processes (`platform-api` and `portal`) serve `GET /metrics` in the Prometheus text format on a **dedicated
metrics listener** (`PLATFORM_METRICS_ADDR`, `PORTAL_METRICS_ADDR`; both required and on a different port from the other
addresses of the process; same server timeouts). One rule for both: `/metrics` is never served on the public, the
internal or the JWKS listener, which answer 404 (tests per process enforce it), because a NetworkPolicy cannot filter by path.
There is no authentication: the NetworkPolicy must allow only the monitoring namespace to reach the metrics port, and
no IngressRoute may route to it. Each process uses its own registry (never the global default one) with the Go runtime and process
collectors, built with `github.com/prometheus/client_golang` (pinned in `go.mod`).

**Cardinality rule (hard).** No label takes a value supplied by a client: not the Host, the path, the user agent,
the query, an email or a user id. Labels take a value from a closed set (decision, reason, outcome, result, status
class), a `ServeMux` route pattern (`Request.Pattern`, never `URL.Path`; a request rejected before the router is
`unmatched`) or a fixed text. The code that records an event clamps its strings to the closed set and reports
anything else as `other`, and a test per process sends thousands of requests with random hosts, paths, methods and
cookies and asserts that the number of series does not move and that none of those values is exposed.
`app_id` is **not** a label anywhere: the set of apps grows with every company that adopts the portal, it would
multiply every other label, and the per-app view already exists (decision log line, `last_seen_at`, probe status).

platform-api (`platform_`):

| Metric | Type | Labels |
|---|---|---|
| `platform_forwardauth_decisions_total` | counter | `decision`, `reason` (the values of the decision log line) |
| `platform_forwardauth_decision_duration_seconds` | histogram | `decision` |
| `platform_forwardauth_cache_lookups_total` | counter | `cache` (`decision`, `host`, `negative_host`), `result` (`hit`, `miss`) |
| `platform_logins_total` | counter | `outcome` (a fake login, development only, counts as `success`): `success`, `provider_refused`, `rejected`, `invalid_flow`, `failed`, `provider_unavailable`, `user_disabled`, `rate_limited`, `invalid_request`, `error` |
| `platform_logouts_total` | counter | `via` (`app`, `auth_host`); only logouts that revoked a session |
| `platform_jwks_requests_total` | counter | `listener` (`jwks`, `public`), `status_class` |
| `platform_signing_key_loaded` | gauge | none; 1 when this replica can sign |
| `platform_signing_key_active_age_seconds` | gauge | none; absent while no signer is loaded |
| `platform_signing_key_kek_mismatch` | gauge | none; 1 when the KEK does not match the active or next keys |
| `platform_lastseen_pending_hosts` | gauge | none |
| `platform_lastseen_dropped_total` | counter | none; `last_seen_at` observations discarded because the queue was full |
| `platform_lastseen_flush_errors_total` | counter | none; failed `last_seen_at` flushes (the batch is kept and retried) |
| `platform_token_use_pending_tokens` | gauge | none; API tokens waiting for the next `last_used_at` flush |
| `platform_token_use_dropped_total` | counter | none; `last_used_at` observations discarded because the queue was full |
| `platform_token_use_flush_errors_total` | counter | none; failed `last_used_at` flushes (the batch is kept and retried) |
| `platform_unknown_host_drops_total` | counter | `reason` (`invalid`, `budget`, `marker`, `queue`) |
| `platform_db_pool_{acquired,idle,total,max}_connections` | gauge | none |
| `platform_db_pool_{acquires,acquires_waited,acquires_canceled}_total`, `platform_db_pool_acquire_wait_seconds_total` | counter | none |

portal (`portal_`):

| Metric | Type | Labels |
|---|---|---|
| `portal_http_requests_total` | counter | `method` (standard methods, else `other`), `route`, `status_class` |
| `portal_http_request_duration_seconds` | histogram | `route` |
| `portal_contention_responses_total` | counter | none |
| `portal_probe_rounds_total` | counter | `result`: `written`, `inconclusive`, `skipped` (another replica holds the lock), `idle` (nothing due), `error` |
| `portal_probe_round_duration_seconds` | histogram | `result` |
| `portal_probe_interval_seconds` | gauge | none; the configured probe interval (`PORTAL_PROBE_INTERVAL`) in seconds, set at startup, `0` = probe disabled; used to gate the probe alerts |
| `portal_probe_last_write_timestamp_seconds` | gauge | none; Unix time (seconds) of the last probe round with result `written`; starts at the process start time and stays there while the probe is disabled |
| `portal_deployments_by_probe_status` | gauge | `status` (`ok`, `unprotected`, `unexpected`, `unreachable`, `never`) |
| `portal_probe_status_count_errors_total` | counter | none |
| `portal_db_pool_*` | as above | none |

Notes:

- `portal_deployments_by_probe_status` is a scrape-time collector: it counts `deployments` by `last_probe_status`
  (`never` for none yet) with a 30 s cache and a 2 s timeout, on every replica and whether or not the probe runs, so
  any replica reports the state of the database. It has no host label. If the count fails the gauge is absent for
  that scrape (a stale value would hide the problem), `portal_probe_status_count_errors_total` grows, and the failure
  is cached for the TTL.
- Counters of a vector with open label combinations (decisions, requests) appear with their first event;
  the small closed sets (logins, logouts, caches, probe rounds) start at zero.
- The pool metrics are read from `pool.Stat()` at scrape time.
- Audit write failures are not a metric: the portal writes the audit row in the same transaction as the change, so a
  failure is the failure of the request itself (`portal_http_requests_total` with `5xx`), and platform-api logs
  the best-effort session audit failures.
- The decision reasons added by the [token path](forwardauth.md#token-path) (`token_no_bearer`, `token_malformed`,
  `token_unknown`, `token_revoked`, `token_expired`, `token_owner_inactive`, `token_scope`) are values of `reason` in
  `platform_forwardauth_decisions_total`; 401 and the scope 403 use decision `unauthenticated` and `deny`.

## `last_seen_at`

- platform-api notes every authorized decision (a 200 with assertion, the portal app included; not denials,
  redirects, preflights or the auth host) in an in-memory map `host -> latest time`. Only hosts that resolved to a
  deployment get there, so the map is bounded by the number of deployments (and hard-capped at 10 000 entries).
  A request never writes to the database.
- A background flusher runs once per minute and writes the whole map with one statement, as `platform_runtime`;
  at most one write per minute per deployment and replica. Times come from the replica that served the request,
  capped at the database clock and never moving the column backwards. Rows are locked in id order with
  `SKIP LOCKED`: a flush never waits for, or deadlocks with, another replica or a deployment removal. The statement
  returns the hosts it skipped (an existing row locked by someone else); they stay in memory, with their time unless
  a newer request touched them, and are written by the next tick. Hosts without deployment and rows that already
  hold a newer time are done, not skipped.
- A failed flush is logged and its batch goes back to memory (newer observations win), to be retried on the next
  tick; it never affects a decision. At shutdown, after the listeners stopped, the flusher makes one last
  best-effort flush bounded to 5 seconds.
- The statement touches neither `host` nor `app_id`, so it emits no `access_changed`.

## Probe

- The portal (not platform-api) probes every deployment host through the public entrypoint without cookies:
  `GET https://<host>/` with `Accept: text/html` and `Sec-Fetch-Mode: navigate` (so ForwardAuth classifies it as a
  navigation), redirects not followed, no cookies kept, 10 seconds per host, 8 hosts at a time. A protected host
  answers a 302 to the auth host login. The response body is never read or logged.
- Anything else means the app is reachable without ForwardAuth (for example an IngressRoute without the
  middleware), the most dangerous failure and one platform-api logs can never show. A matching check belongs in
  gitops CI.
- Classification, stored in `deployments.last_probe_status` together with `last_probe_at`:

| Result | Meaning |
|---|---|
| `ok` | 302, 303 or 307 (the temporary redirects ForwardAuth uses) whose `Location` is `https://<auth host>/login...` (auth host from `PORTAL_ISSUER`; host, including port, compared case-insensitively; path exactly `/login`) |
| `unexpected` | Any other redirect: 301 or 308, another host or path, `http`, credentials in the URL, a relative `Location` (it resolves against the deployment host), or a redirect without `Location` |
| `unprotected` | Any response that is not a redirect, whatever its status (2xx, 4xx, 5xx): the host was served without ForwardAuth. A 401, 403 or 503 counts too: ForwardAuth answers a cookie-less navigation with a 302. A platform-api outage would look the same, which is what the control sample below prevents |
| `unreachable` | DNS, TCP, TLS (including a certificate that does not verify) or timeout: nothing can be said about the host |

A host that has a deployment but no Traefik router answers a 404 from Traefik itself, and classifies as
`unprotected` too: nothing in the response tells the two apart. In the health report the combination `never_seen`
plus `unprotected` points to a missing or wrong route (no authorized traffic ever arrived); `unprotected` on a host
that was seen means a route without the ForwardAuth middleware.

- Every `unprotected` and `unexpected` result logs a WARN with host, app id and HTTP status.
- Configuration (`PORTAL_PROBE_INTERVAL`, `PORTAL_PROBE_ADDR`, `PORTAL_PROBE_CA_FILE`, `PORTAL_PROBE_INSECURE_TLS`): [configuration.md](configuration.md#portal).
- Single runner: every replica runs the loop, but a round takes a session-level PostgreSQL advisory lock (try, never
  waits) on a dedicated connection and probes only the deployments never probed or last probed more than half an
  interval ago (database clock). A replica that wakes up while another one holds the lock, or just after it
  finished, finds nothing to do. Results are written with one statement (same `SKIP LOCKED` rules as above) and emit
  no `access_changed`. The lock is released when the round ends, or by the database if the connection dies.
- Control sample: every round starts by probing the first host (by name) of the portal app (`PORTAL_APP_ID`), which
  is known to sit behind ForwardAuth, whether or not it is due. If the control result is not `ok`, the round is
  inconclusive: a platform-api or Traefik outage would make every host look `unprotected` or `unreachable`. Nothing
  is stored (the previous statuses stay), a WARN logs the control status and host, and the next tick retries. If the
  portal app has no deployment the round is skipped the same way, with a WARN. When nothing is due the control is
  not probed. The control is probed again at the end of the round, and if it is no longer `ok` the whole round is
  discarded (WARN, nothing stored): an outage that started during the round taints the results already measured.
  Results are therefore written once, after that second check, and the WARN per `unprotected` or `unexpected` host
  is logged then; a process killed in the middle of a round (SIGKILL) loses that round, which the next tick
  repeats.
- A round pins one pooled connection for its whole duration (the advisory lock), so `portal serve` raises its
  connection pool to at least 8 (a larger configured size is kept).
- The portal needs network egress to `PORTAL_PROBE_ADDR` over HTTPS. Because the dial address is fixed, the portal
  never resolves the host names that admins write into `deployments` (no blind SSRF through the probe): they only
  travel as SNI and `Host`.

## Health of an app

`GET /api/v1/apps/{id}/health` (action `app.health`, scoped to the app's initiative: platform admins and the
managers of that initiative; anyone else gets 404). It is a read: no audit row, no side effect. The answer holds:

- `app`: the app with its deployments, each with `last_seen_at`, `last_probe_at` and `last_probe_status` (`null`
  means never; the same three fields appear in every app listing);
- `initiative_status`;
- `grant_count`: non-expired grants of the initiative, whoever holds them;
- `effective_manager_grants`: `manages_access` grants that are effective by the definition of the last-manager rule
  (same SQL expression);
- `problems`: a list of `{code, host}` (`host` is `null` for findings about the app as a whole), app-level findings
  first and then each deployment in host order;
- `checked_at`.

| Code | Scope | When |
|---|---|---|
| `app_disabled` | app | `apps.status` is `disabled` |
| `initiative_inactive` | app | the initiative is `inactive` |
| `no_grants` | app | `grant_count` is 0 |
| `no_manager` | app | `effective_manager_grants` is 0 |
| `unprotected` | host | last probe `unprotected` |
| `unexpected_redirect` | host | last probe `unexpected` |
| `unreachable` | host | last probe `unreachable` |
| `never_probed` | host | no probe result yet (probe enabled) |
| `probe_stale` | host | `last_probe_at` older than 3 probe intervals (probe enabled): rounds are not completing, for example the control sample keeps failing |
| `probe_disabled` | app | the portal runs with `PORTAL_PROBE_INTERVAL=0`; replaces `never_probed` and `probe_stale` (a status stored earlier is still reported) |
| `never_seen` | host | `last_seen_at` is null |
| `not_seen_recently` | host | `last_seen_at` older than 24 hours |

`never_seen` and `not_seen_recently` are not reported while the app is disabled or its initiative inactive: nobody
can be authorized, so no traffic is expected. Probe findings are always reported.

## Orphan hosts

An orphan host belongs to no app, hence to no initiative, so it is not part of any app's health.
`GET /api/v1/health/orphan-hosts` (action `orphan_host.list`, platform admins only; others get 403) lists the
hosts with `unknown_host` denials in the last 7 days that have no deployment now (a host that was added afterwards
is no longer an orphan), most recent denial first, at most 100: `{hosts: [{host, count, last_denied_at}]}`. `count`
is the number of recorded denials; platform-api records at most one per host every 10 minutes per replica ([ForwardAuth](forwardauth.md#request-classification-and-edge-rules)),
so it measures how long a host has been hit, not how many requests it got. The query reads `access_denials`, which
the `platform_portal` role may select; no extra index is kept: `unknown_host` rows are bounded to 30 per minute per replica ([ForwardAuth](forwardauth.md#request-classification-and-edge-rules)), so a week holds at most about 300 000 per replica, and the endpoint is admin-only.
