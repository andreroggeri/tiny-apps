# Superlogica login service (not implemented)

Pombo books with a logged-in session but doesn't log in itself. When the
session is missing or expired, it asks this service for a new one. Until the
service exists, a session can be pasted in the dashboard instead.

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
- Handle one login at a time. Pombo never sends two at once, but a second
  call while one is running should wait or fail, not start a parallel login.

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
  repo). Give it to the service as an env var, never in code.

## How to validate

1. Run the service locally, then:

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
