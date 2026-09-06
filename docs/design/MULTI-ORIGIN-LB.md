# Single-edge multi-origin load balancing

Status: Proposed
Last updated: 2026-06-05

## Summary

Allow multiple origin machines to be registered behind a single tunnel /
subdomain, with Gopher generating a Caddy `reverse_proxy` block that
load-balances across them with health checking. This is a thin orchestration
layer over Caddy's built-in load balancer.

## Motivation

Redundancy and horizontal scale for a single service: run two (or more) replicas
of an app on separate boxes behind `app.domain.com`, and have requests spread
across the healthy ones with automatic ejection of unhealthy upstreams.

## Design sketch

- **Data model:** lift the current 1:1 (service tunnel → machine) to 1:N, so a
  service tunnel can reference a pool of origin machines.
- **Config generation:** emit a multi-upstream
  [`reverse_proxy`](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)
  block with:
  - `lb_policy` (e.g. `round_robin`, `least_conn`),
  - active health checks (`health_uri`, `health_interval`),
  - passive ejection (`fail_duration`, `max_fails`).
- **Leverage Caddy's balancer; do not reimplement it.** No custom
  load-balancing logic in Go on the request path — Gopher's job is to generate
  the correct `reverse_proxy` block, nothing more. (Same boundary as the SSO
  work: orchestrate the proven component, don't rebuild it.)
- **Liveness:** the agent's `WatchStatus` stream can drive the dashboard's
  origin-health view, but request-level failover is Caddy's responsibility, not
  the agent's.

## Scope / non-goals

- This covers load balancing **behind a single edge node**. Coordinating an
  origin pool across *multiple* edge nodes (keeping every edge's view of the
  healthy set consistent) is a separate architectural concern and is **out of
  scope for this issue**.

## Open questions

- Default `lb_policy`.
- How `health_uri` is configured per service (sane default + per-tunnel
  override?).
- Dashboard UX for adding/removing machines from a service's origin pool.
