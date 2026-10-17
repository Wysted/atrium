# Runbook: rotate the Google OAuth client secret

Scope: the `client_secret` that platform-api uses to exchange the authorization code at Google's token endpoint
(`PLATFORM_GOOGLE_CLIENT_SECRET_FILE`). The client id does not change. Commands assume the local k3d cluster
(`./tk`, overlay `overlays/local-google`); in another cluster, replace the Secret creation step with your secret manager
and keep the rest.

## How platform-api loads the secret

- Mounted from Secret `platform/platform-google-client-secret`, key `secret`, at `/etc/platform/google/secret`
  (`fsGroup` + `defaultMode: 0440`, same policy as the KEK).
- Read ONCE at start-up (`loadGoogleConfig`, trimmed of whitespace). The kubelet refreshes the mounted file after a
  Secret update, but the process never re-reads it. **A `rollout restart` is mandatory.**
- Start-up only checks that the file is non-empty. It does not call Google, so a wrong secret still gives Ready pods and
  fails at the first login. **Verification must be a real login.**
- Two replicas, rolling update: during the rollout old pods keep the old secret and new pods use the new one. That is why
  both Google secrets must be enabled until the rollout is complete.

## When to rotate

- Schedule: at least once a year, or whenever the person who had console access leaves.
- Suspected leak (secret in a log, ticket, chat, laptop): use the [leak variant](#leak-variant).
- Rarely used installs: see [6-month inactivity](#6-month-inactivity-risk).

## Prerequisites

- A Google Cloud account with permission to edit the OAuth client (Google Auth Platform > Clients).
- Operator with `./tk` and the repo checkout; Grafana access (admin) to read metrics.
- The new value is shown ONLY at creation (afterwards only the last 4 characters are visible), so download or copy it
  immediately. Never paste it into a command line, ticket or chat.

## Google facts (verified 2026-10-16)

- A Web client holds at most 2 secrets; both work until one is disabled. Actions per secret: Add, Disable, Enable, Delete
  (Delete only on a disabled secret, irreversible). Disable takes effect "shortly" (no documented time); Enable reverts it.
- Secrets are stored hashed; the full value is shown/downloadable only at creation.
- No supported API or `gcloud` command for Web client secrets: the console step is manual.
- Failures at the token endpoint: `invalid_client` ("The OAuth client secret is incorrect.") and `deleted_client`. The
  exact code for a disabled secret is not documented; expect `invalid_client`.
- Sources: https://support.google.com/cloud/answer/15549257 ,
  https://support.google.com/cloud/answer/6158849 ,
  https://developers.googleblog.com/en/usability-and-safety-updates-to-google-auth-platform/ ,
  https://developers.google.com/identity/protocols/oauth2/web-server

## Steps

If the client already has 2 secrets, disable and delete the older one first (after checking it is not the one deployed).

1. **Add the new secret.** Console: Google Auth Platform > Clients > the client > "Client secrets" > Add Secret. Download or
   copy it now.
2. **Store it locally** (file only, no shell history):

   ```bash
   cp -p .tools/secrets/google-client-secret .tools/secrets/google-client-secret.old   # rollback copy
   ${EDITOR:-vi} .tools/secrets/google-client-secret                                    # replace the content with the new value
   chmod 600 .tools/secrets/google-client-secret
   ```

3. **Update the Secret and restart** (same command as `overlays/local-google/up.sh`):

   ```bash
   ./tk sh -c "kubectl -n platform create secret generic platform-google-client-secret \
     --from-file=secret=.tools/secrets/google-client-secret --dry-run=client -o yaml | kubectl apply -f - >/dev/null"
   ./tk kubectl -n platform rollout restart deploy/platform-api
   ./tk kubectl -n platform rollout status deploy/platform-api --timeout=240s
   ```

   Do not rely on `local-google/up.sh` alone: without `BUILD=1` it updates the Secret but does not restart.
4. **Verify a real login end to end.** In a private browser window open a protected app (for example
   `https://hello-a.apps.wysted.dev`), sign in with a Workspace account of the allowed domain and confirm you land on the
   app. Repeat once more (the two replicas may differ only if the rollout is incomplete; it is not at this point).
5. **Check metrics and logs** (see [Detecting failures](#detecting-failures)): `platform_logins_total{outcome="success"}`
   increased and there is no `failed`.
6. **Disable the old secret.** Console > "Client secrets" > the OLD secret (match by creation date / last 4 characters)
   > Disable. Do NOT delete yet.
7. **Observation window: 24 h** (at least one business day with real logins). Watch for `outcome="failed"` and log lines with
   `invalid_client`. Do a real login again right after disabling and again at the end of the window.
8. **Delete the old secret** (trash icon on the disabled secret) once the window is clean. Then remove the local copy:

   ```bash
   shred -u .tools/secrets/google-client-secret.old 2>/dev/null || rm -f .tools/secrets/google-client-secret.old
   ```

## Detecting failures

platform-api answers a rejected code exchange (any 4xx from the token endpoint, which includes `invalid_client`) with a
generic 400 page and counts it as `outcome="failed"`. A 5xx or 429 from Google counts as `provider_unavailable` (503).

Logs (JSON, level WARN):

```bash
./tk kubectl -n platform logs -l app.kubernetes.io/name=platform-api --since=30m --tail=-1 | rg 'google login failed'
# {"level":"WARN","msg":"google login failed: rejected","error":"authorization code exchange failed: token endpoint answered 401 invalid_client"}
```

The log carries only the HTTP status and the OAuth error code; never the response body or the secret. The status shown for
`invalid_client` is what Google returns (typically 401); match on the `invalid_client` text, not the number.
`invalid_grant` in the same line is a different problem (expired or reused code, not the secret).

Metrics (Grafana Explore, Prometheus datasource; the metrics port is only open to Prometheus):

```promql
sum by (outcome) (increase(platform_logins_total[15m]))
```

There is NO alert for failed logins today (`monitoring/rules/portal-platform.rules.yaml` only covers ForwardAuth, signing
keys, the probe and pool saturation). `ForwardAuthDown` or `ForwardAuthErrorsHigh` will NOT fire for a wrong secret:
sessions that already exist keep working and only new logins fail. During rotation the operator is the alert: check the
dashboard panel "Logins by outcome" and the log query above. A persistent `failed` rate with
`invalid_client` after a rotation means the deployed secret is disabled, deleted or wrong.

## Rollback

Use it when step 4 or 5 fails, or during the observation window after disabling the old secret.

1. If the old secret is disabled: Console > Enable it (reverts the disable; works while it is not deleted).
2. Restore the previous value and restart:

   ```bash
   mv .tools/secrets/google-client-secret.old .tools/secrets/google-client-secret
   ./tk sh -c "kubectl -n platform create secret generic platform-google-client-secret \
     --from-file=secret=.tools/secrets/google-client-secret --dry-run=client -o yaml | kubectl apply -f - >/dev/null"
   ./tk kubectl -n platform rollout restart deploy/platform-api
   ./tk kubectl -n platform rollout status deploy/platform-api --timeout=240s
   ```

3. Verify a real login. Investigate the new secret (copy error, trailing characters, wrong client) before retrying.

Once the old secret is deleted there is no rollback: add another secret and repeat from step 1.

## Leak variant

Goal: stop the leaked secret working as fast as possible; brief login failures are acceptable (existing sessions are
not affected, only new logins).

1. Add the new secret (step 1) and deploy it (steps 2-3). Skip the observation window.
2. Verify a real login (step 4).
3. Immediately Disable the leaked secret in the console.
4. Verify a real login again and check `failed` / `invalid_client` for a few minutes. Failures right after step 3 can come
   only from pods still running the old secret; if the rollout completed in step 3 there should be none.
5. If the leaked secret was already at the 2-secret limit, delete the other (non-leaked, undeployed) secret first.
6. Delete the leaked secret after the checks, run the post-checks, and find out where the leak happened.

If the new secret cannot be deployed quickly (cluster down), disabling the leaked one first is still valid: logins fail
until the new secret is deployed.

## Post-checks

- `./tk kubectl -n platform get pods -l app.kubernetes.io/name=platform-api` shows 2/2 Ready with fresh ages.
- Exactly the intended secret is enabled in the console (the "Client secrets" list shows one enabled entry, last 4
  characters match the new one).
- A real login works; `outcome="success"` rises, `failed` stays flat.
- No copy of the old or new secret remains outside `.tools/secrets/google-client-secret` (downloads folder, clipboard
  history, shell history).
- Record the rotation date (next rotation due in 12 months).

## 6-month inactivity risk

Google deletes an OAuth client automatically after 6 months without use (logins or settings changes count as activity)
and emails the owners 30 days before. A deleted client is restorable for about 30 days. For rarely used installs, keep
the notification email monitored and make at least one login or a settings change within each 6-month period. If the
token endpoint starts answering `deleted_client`, restore the client in the console first; a new secret will not help.
