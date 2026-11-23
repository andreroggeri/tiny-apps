# Handoff: Superlogica login for pombo

State as of 2026-11-22. Branch `feat/pombo-reservas`, PR andreroggeri/tiny-apps#1.

## Goal

Pombo books the condo's manicure chair from WhatsApp. It needs a logged-in
session on the condo site, and renews that session itself when it expires.

**The login works and is verified against the real site.** What's left is the
deploy.

## What works

| Piece | Where | State |
|---|---|---|
| Booking handler (`reservas`) | `internal/handlers/reservas` | Done, tested. Uses the local LLM to read messages. |
| Site client (book, cancel, keep-alive) | `internal/superlogica` | Done. Verified against the real site. |
| Session paste in dashboard | `internal/dash` ("Condomínio" box) | Done. Now also how a session is seeded; refuses a paste missing the Keycloak cookies. |
| **Session renewal (`SSOLogin`)** | `internal/superlogica/sso.go` | **Done, tested. Runs inside pombo — no sidecar, no browser.** |
| Hand tool / HTTP wrapper | `cmd/superlogica-login` | Done. Not deployed; for seeding and local checks. |
| Checking tool | `cmd/superlogica-check` | Done. `-keep` leaves a test booking in place. |
| otp-relay Worker + Gmail filter | infrastructure repo | Live, but **no longer used**. See [OTP-RELAY.md](OTP-RELAY.md). |

## How renewal works

The login form is behind reCAPTCHA Enterprise, so it is never submitted. The
site keeps two sessions and only the short one expires:

| Session | Host | Lives |
|---|---|---|
| `PHPSESSID` | `your-condo.superlogica.net` | short |
| Keycloak SSO | `sso.superlogica.com` | much longer |

Opening the condo site with the Keycloak cookies attached makes the OIDC
authorize endpoint redirect straight back to the callback and mint a new app
session — no form, no reCAPTCHA, no emailed code. Plain `net/http`.

A renewal returns the cookies for both hosts and pombo saves them all to
`/data/superlogica-session.json`, so that one file keeps the next renewal
possible. Details in [README.md](README.md).

This replaced the original plan (a sidecar driving a real browser through the
email + code flow). That needed a browser in the cluster — the storage problem
these notes used to flag — and a stealth browser to beat the reCAPTCHA score,
which isn't a road worth taking for a condo booking bot.

## Verified on the real site (2026-11-22)

- Keycloak answered an authorize request carrying the SSO cookies and
  `prompt=none` with a `code=`, not `error=login_required`: the realm does do
  silent re-auth.
- `superlogica-check` against the renewal: `OK: session is logged in`, 14
  cookies in 3 s.
- A real booking went through on an early-morning slot, confirmed in the
  portal, then cancelled.
- The saved session after a renewal holds 3 condo cookies and 11 Keycloak
  ones, so it is self-sustaining.

## Deploy

1. Merge PR #1. CI publishes `ghcr.io/andreroggeri/tiny-apps/pombo:sha-<commit>`.
2. In the infrastructure repo, `k3s/applications/pombo/values.yaml`: bump
   `image.tag`, and set `SUPERLOGICA_URL`, `SUPERLOGICA_CONDO_ID` and
   `SUPERLOGICA_UNIT_ID` — **they no longer have defaults in the repo and
   pombo now refuses to start without them.** Nothing else: no sidecar, no
   second image, no extra volume, no `SUPERLOGICA_LOGIN_URL`.
3. `cd k3s && helmfile -l name=pombo --skip-deps sync`
4. Seed the session: log in by hand and paste the cookies for **both** hosts
   into the dashboard's "Condomínio" box. A paste without the
   `sso.superlogica.com` cookies is refused, so this is hard to get wrong now.
5. Route the family group to `reservas` in the dashboard.
6. Add a line to `apps/pombo/DECISIONS.md`.

## The idle-timeout risk, and why it's the opposite of what it looks like

Keycloak's SSO session has its own idle timeout. The trap: **pombo's
keep-alive does not touch it.** `Alive()` fetches
`/clients/areadocondomino/reservas` on the *condo* host, refreshing
`PHPSESSID` only. `sso.superlogica.com` is reached solely when a renewal
runs.

So the failure mode shows up when everything is working, not when pombo is
down: a healthy keep-alive means the app session never dies, which means no
renewal ever runs, which means the Keycloak session sits untouched until it
idles out. The first time a renewal is actually needed, it fails.

Fix, if the first weeks show this: touch the SSO session on a timer. An
authorize request with `prompt=none` resets Keycloak's idle clock, costs one
round trip and mints no app session. It needs no valid PKCE verifier, since
the returned code is thrown away.

## Things nobody knows yet

- **How long the Keycloak SSO session lasts, idle.** The number that matters:
  it decides whether the timer fix above is needed and how often a hand
  re-seed is. Keycloak's own defaults are short (30 min idle, 10 h max), but
  realms with "remember me" run far longer, and this one clearly does — the
  session used on 2026-11-22 was days old and still re-authed silently.
  Superlogica's realm config isn't visible from outside; only the logs will
  tell.
- How long an app session lasts. Pombo logs `superlogica session expired
  lasted_at_least=...` each time; check after the first week.
- Whether a booking at midnight gets the slot first; the first real run will
  tell.

## Site facts

- Condo and unit IDs are deployment config (`SUPERLOGICA_CONDO_ID`,
  `SUPERLOGICA_UNIT_ID`), deliberately not in this repo.
- Manicure chair: one area ID per 2-hour slot, eight slots from 06h to 22h.
  The IDs are contiguous; read them off the booking page.
- Dates open 30 days ahead at 00:00 São Paulo time; earliest is tomorrow.
- Booking and cancel requests: `internal/superlogica/client.go`.
- The login page runs reCAPTCHA Enterprise (site key in the page source) and
  Datadog RUM, and posts `hidden_validation` with the reCAPTCHA token. The
  page's own script notes that a submit without a token gets an error back.
