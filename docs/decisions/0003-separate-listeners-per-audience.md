# 0003. Separate listeners per audience

- Status: Accepted
- Date: 2026-10-14

## Context and Problem

platform-api must expose `/forwardauth` only to Traefik, the JWKS only to apps, browser routes only through the auth host route and `/metrics` only to monitoring. A Kubernetes NetworkPolicy filters by port, never by path.

## Considered Options

- One listener with path-based routing: a path cannot be firewalled by NetworkPolicy, so Traefik or the apps would reach routes they must not.
- One listener per audience (chosen).

## Decision Outcome

platform-api serves four listeners: internal (`/forwardauth`, `/healthz`, `/readyz`), jwks, public (auth host browser routes) and metrics. Their addresses (`PLATFORM_INTERNAL_ADDR`, `PLATFORM_PUBLIC_ADDR`, `PLATFORM_METRICS_ADDR`, `PLATFORM_JWKS_ADDR`) are required and must use different ports. App namespaces reach a port that serves nothing but the JWKS. The portal follows the same rule for its metrics (`/metrics` is never served on another listener).

## Consequences

- Four ports and four NetworkPolicy rules to maintain; a wildcard host conflicts with any host on the same port, so the ports must differ.
- `/metrics` has no authentication of its own: the NetworkPolicy must allow only the monitoring namespace, and no IngressRoute may route to it.
- Reference: [Listeners](../reference/forwardauth.md#listeners).
