# Sourced by up.sh, smoke.sh and browser-check/browser-check.sh. local_ca_detect <repo root> sets LOCAL_CA to
# .tools/secrets/local-ca.crt when the cluster really is in local CA mode, else to "". Local CA mode needs BOTH the file
# (written by local/certs-up.sh) AND the wildcard Certificate issued by ClusterIssuer local-ca; a file left over from a
# previous mode would make the scripts trust a CA that signs nothing, so it is an error.
local_ca_detect() {
  local root="$1" file="$1/.tools/secrets/local-ca.crt" issuer
  LOCAL_CA=""
  [[ -s "$file" ]] || return 0
  issuer="$("$root/tk" kubectl -n traefik get certificate apps-wildcard --ignore-not-found -o jsonpath='{.spec.issuerRef.name}')" ||
    { echo "cannot read the apps-wildcard Certificate (is the cluster up?)" >&2; exit 1; }
  [[ "$issuer" == local-ca ]] ||
    { echo "stale .tools/secrets/local-ca.crt (wildcard issuer: '${issuer:-none}'): re-run local/certs-up.sh" >&2; exit 1; }
  LOCAL_CA="$file"
}
