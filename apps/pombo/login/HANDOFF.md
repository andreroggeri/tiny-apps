# Handoff: Superlogica login for pombo

State as of 2026-11-22. Branch `feat/pombo-reservas`, PR andreroggeri/tiny-apps#1.

## Goal

Pombo books the condo's manicure chair from WhatsApp. It needs a logged-in
session on the condo site. When the session expires, pombo calls a login
service to get a new one. **That login is the only missing piece.**

## What already works

| Piece | Where | State |
|---|---|---|
| Booking handler (`reservas`) | `internal/handlers/reservas` | Done, tested. Uses the local LLM to read messages. |
| Site client (book, cancel, keep-alive) | `internal/superlogica` | Done, tested against fakes; not yet against the real site. |
| Session paste in dashboard | `internal/dash` ("Condomínio" box) | Done. Manual fallback. |
| Login service HTTP server | `cmd/superlogica-login` | Done. Answers `POST /login`. |
| Reading the emailed code | `waitForCode` in `cmd/superlogica-login` | Done, tested. |
| Code mailbox | `otp-superlogica@roggeri.com.br` → Cloudflare Worker → `https://otp.roggeri.com.br/codes/superlogica` | Live. Gmail forwarding confirmed. Code in `infrastructure/pulumi/homelab/otp-relay`. |
| Gmail filter for Superlogica code emails | Gmail | Done. |
| **`login()`** | `cmd/superlogica-login/main.go` | **TODO** |

## The login flow

1. Pombo's keep-alive (every 5 min) or a booking finds the session dead.
2. Pombo sends `POST http://localhost:8000/login` (`SUPERLOGICA_LOGIN_URL`).
3. `login()` opens a browser at
   `https://kpgadministrado.superlogica.net/clients/areadocondomino`, which
   redirects to `sso.superlogica.com` (Keycloak, realm `superlogica-cloud`).
4. Note the time, type `SUPERLOGICA_EMAIL`, continue. The site emails a code.
5. Gmail forwards the email to the Worker. `waitForCode(ctx, cfg, askedAt)`
   polls it and returns the code (about 2 s between polls, 3 min timeout).
6. Type the code. Wait until the page is back on
   `kpgadministrado.superlogica.net/clients/areadocondomino/...`.
7. Return all browser cookies (both domains) as `[]superlogica.Cookie`
   (`name`, `value`, `domain`, `path`). The server sends them to pombo as JSON.
8. Pombo saves them (`/data/superlogica-session.json`) and continues.

Must finish within 5 minutes (pombo's timeout). On failure, return an error;
the server answers 500 with the message and pombo tells the group.

## Run and check it locally

```sh
cd apps/pombo
# token: in the infrastructure repo, pulumi/:  pulumi stack output otpRelayToken --show-secrets
SUPERLOGICA_EMAIL=a.roggeri.c@gmail.com OTP_RELAY_TOKEN=... go run ./cmd/superlogica-login
```

In another terminal:

```sh
go run ./cmd/superlogica-check -login-url http://localhost:8000/login
# expect: OK: session is logged in

# real booking test: books slot 788 (06–08h) and cancels it right away.
# use an open date: tomorrow up to 30 days ahead.
go run ./cmd/superlogica-check -login-url http://localhost:8000/login -book 788 -date YYYY-MM-DD
```

Unit tests: `make lint test`.

## Deploy (after login() works)

1. Merge PR #1. CI publishes `ghcr.io/andreroggeri/tiny-apps/pombo:sha-<commit>`.
2. Build an image for the login service (the Go binary plus the browser it
   drives). Pombo's Dockerfile only builds `cmd/pombo`; the login service
   needs its own.
3. In the infrastructure repo:
   - Add the login service as a sidecar in `k3s/charts/pombo/templates/deployment.yaml`,
     with `SUPERLOGICA_EMAIL` and `OTP_RELAY_TOKEN` from a sealed secret
     (`utils/seal-secret.sh`).
   - In `k3s/applications/pombo/values.yaml`: bump `image.tag`, set
     `superlogica.loginUrl: http://localhost:8000/login`.
   - `cd k3s && helmfile -l name=pombo --skip-deps sync`
4. In pombo's dashboard, route the family group to `reservas`.
5. Add a line to `apps/pombo/DECISIONS.md` for the deploy.

Storage is tight on the cluster. The browser is most of the sidecar's size.

## Site facts

- Condo `58`, unit `6471`.
- Manicure chair: one area ID per 2-hour slot, 788 (06–08h) to 795 (20–22h).
- Dates open 30 days ahead at 00:00 São Paulo time; earliest is tomorrow.
- Booking and cancel requests: `internal/superlogica/client.go`.

## Things nobody knows yet

- How long a session lasts. Pombo logs `superlogica session expired
  lasted_at_least=...` each time; check after the first week.
- The exact sender of Superlogica's code emails (the Gmail filter matched it).
- Whether a booking at midnight gets the slot first; the first real run will tell.
