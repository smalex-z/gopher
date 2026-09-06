# Edge SSO via Caddy `forward_auth`

Status: Proposed
Last updated: 2026-06-05

## Summary

Add an optional per-tunnel authentication gate in front of services by
orchestrating Caddy's built-in [`forward_auth`](https://caddyserver.com/docs/caddyfile/directives/forward_auth)
directive against a user-chosen external identity provider (IdP). Gopher
generates the Caddy config and wires the IdP; **Gopher does not implement
OAuth2/OIDC, session management, or token validation itself.**

## Motivation

Users routinely want to put internal services — dashboards, wikis, admin
panels, staging environments — behind a login without exposing them raw to the
internet. `forward_auth` + a dedicated IdP is the standard, well-audited pattern
for this, and it slots naturally into Gopher's existing edge.

## Non-goals

- Gopher will **not** ship its own identity provider, OAuth2/OIDC server, or
  cookie/session/token logic. Auth correctness is delegated to the IdP — the
  same reasoning we apply to TLS and tunneling (trust a focused, scrutinized
  component rather than reimplementing a footgun-heavy primitive).

## Design sketch

- **Per-tunnel toggle** "Require authentication", parallel to the existing
  bot-protection toggle on a tunnel.
- **Config generation:** the tunnel's Caddy block gains a `forward_auth` block
  pointing at the configured IdP's verify endpoint, with the standard
  `copy_headers` / redirect handling. This is pure config-gen over a stock Caddy
  directive — no custom Go on the request path.
- **Recommended default IdP: [Authelia](https://www.authelia.com/)** —
  Apache-2.0, single lightweight Go binary, `forward_auth` is its native use
  case, and it has no paid tier to accidentally depend on. Also support
  [Authentik](https://goauthentik.io/) (its OIDC + proxy/forward-auth providers
  are in the open-source edition) and
  [oauth2-proxy](https://github.com/oauth2-proxy/oauth2-proxy) (bring-your-own
  upstream IdP such as Google/GitHub) as alternatives. Gopher's value is the
  orchestration — discovery, callback URLs, the auth subdomain, the generated
  Caddy block — not the IdP itself, so supporting more than one is cheap.

## Relationship to bot protection

Conceptually parallel (an edge gate that runs before the request reaches the
origin) but **mechanically different**, and the two must stay separate:

- **Bot protection** runs *inside Gopher's own process* (`internal/proxy`):
  Caddy reverse-proxies the protected host to Gopher, Gopher runs the PoW gate,
  then forwards.
- **`forward_auth` SSO** runs *inside Caddy*: the directive calls out to the IdP
  and Caddy gates before proxying.

Do not try to merge them into one mechanism.

## Open questions

- Do we ship an opinionated Authelia bootstrap/install helper, or document
  bring-your-own and only generate the Caddy wiring?
- How is the auth subdomain provisioned (its own tunnel? a reserved host?).
- Per-tunnel IdP vs one IdP for the whole deployment for the first cut.
