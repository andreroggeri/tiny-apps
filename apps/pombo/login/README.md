# Superlogica login service (not implemented)

Pombo books with a logged-in session but doesn't log in itself. When the
session is missing or expired, it asks this service for a new one. Until the
service exists, a session can be pasted in the dashboard instead.

The service is `cmd/superlogica-login`, a separate Go binary so the browser it
needs stays out of pombo's image. Its HTTP server and the code lookup
(`waitForCode`) are done; `login()` is the part left to write. Run it with:

```sh
SUPERLOGICA_EMAIL=... OTP_RELAY_TOKEN=... go run ./cmd/superlogica-login
```

The site's login is email + a code sent by email, at
`sso.superlogica.com` (realm `superlogica-cloud`, client
`cloud_areadocondomino`). After it, the browser lands on
`https://kpgadministrado.superlogica.net/clients/areadocondomino/...`.

## What it must do

- Listen for `POST /login` (any path works; it's whatever
  `SUPERLOGICA_LOGIN_URL` points to). No request body.
- Log in, then answer **200** with a JSON array of the browser's cookies, in
  Playwright's `context.cookies()` shape. Extra fields are ignored:

  ```json
  [{"name": "PHPSESSID", "value": "…", "domain": "kpgadministrado.superlogica.net", "path": "/"}]
  ```

  Return all cookies, not only `PHPSESSID`: the ones for
  `sso.superlogica.com` may let pombo's next login skip the code.
- Answer within **5 minutes** (pombo's timeout), including the wait for the
  emailed code.
- On failure, answer non-200 with a short plain-text reason. Pombo logs it and
  tells the group a login is needed.
- Handle one login at a time (the stub answers 409 to a second call).

## Getting the emailed code

A Gmail filter forwards Superlogica's code emails to
`otp-superlogica@roggeri.com.br`. A Cloudflare Worker (`otp-relay`, in the
infrastructure repo under `pulumi/homelab/otp-relay`) keeps the latest one for
10 minutes. Poll it right after asking for the code:

```
GET https://otp.roggeri.com.br/codes/superlogica?since=<epoch ms when you asked>
Authorization: Bearer <token>
```

- `404` until an email arrives after `since`; poll every 2–3 s.
- `200` with `{"code": "482913", "from": "...", "subject": "...", "receivedAt": 1795..., "text": "..."}`.
  `code` is `null` if no 4–9 digit number was found; read `text` then.
- Token: `pulumi stack output otpRelayToken --show-secrets` (infrastructure
  repo). The service reads it from `OTP_RELAY_TOKEN`, never from code.
- `waitForCode(ctx, cfg, askedAt)` in the stub already does this polling.

## How to validate

1. Run the service locally (above), then:

   ```sh
   go run ./cmd/superlogica-check -login-url http://localhost:8000/login
   ```

   Expect `OK: session is logged in`.
2. Test a real booking with the new session (books, then cancels at once).
   Use an open date, from tomorrow up to 30 days ahead:

   ```sh
   go run ./cmd/superlogica-check -login-url http://localhost:8000/login -book 788 -date 2026-12-20
   ```

3. Deploy it next to pombo, set `SUPERLOGICA_LOGIN_URL`, delete pombo's saved
   session (`/data/superlogica-session.json`) and restart. The pombo log
   should show `superlogica session renewed`.
