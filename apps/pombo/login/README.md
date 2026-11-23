# Superlogica login

Pombo renews its own condo-site session, in-process. There is no login
service to deploy and no browser anywhere.

The work is `SSOLogin` in `internal/superlogica/sso.go`, wired in
`cmd/pombo/main.go`. `cmd/superlogica-login` is the same thing behind an HTTP
server, kept as a hand tool for seeding and for checking a renewal without
running pombo.

## How it gets a session

The site's login form is behind **reCAPTCHA Enterprise**. `script.js` on the
login page puts a token in the form's `hidden_validation` field at submit
time, and the server rejects a submit without one. That form can't be filled
in from here, so the service doesn't try.

It doesn't have to. The login is two sessions, not one:

| Session | Where | Lives |
|---|---|---|
| App session (`PHPSESSID`) | `kpgadministrado.superlogica.net` | short |
| Keycloak SSO session | `sso.superlogica.com` | much longer |

Only the **app** session is what keeps expiring. Opening the condo site with
the Keycloak cookies still attached makes the OIDC authorize endpoint answer
with a redirect straight back to the callback instead of rendering a form:

```
GET /clients/areadocondomino
 -> 302 sso.superlogica.com/auth/realms/superlogica-cloud/.../auth?...
 -> 302 back to /clients/areadocondomino/publico/oidccallback?code=...
 -> 302 /clients/areadocondomino/...   (new PHPSESSID)
```

No form is rendered, so no reCAPTCHA is involved. Plain `net/http`, no
browser, no extra dependency.

The renewed session is checked before being used (it must open the
reservations page), so a bad one is reported at the source rather than
failing at the next booking.

**One file holds everything.** A renewal returns the cookies for *both*
hosts, and pombo saves them all to `/data/superlogica-session.json`. The
Keycloak cookies in that file are what make the *next* renewal possible, and
they rotate each time. So a working pombo keeps itself going with no seed
file, no second volume and no `SUPERLOGICA_LOGIN_URL`.

The cost: the **first** login is done by hand, and again if the Keycloak SSO
session itself ever expires. See *Seeding* below.

## Settings

Pombo needs none of its own for this; it uses the session it already saves.
`SUPERLOGICA_LOGIN_URL` stays as an escape hatch: set it and pombo asks an
external service instead, the way it used to.

For the hand tool, `cmd/superlogica-login`:

| Env | Default | What |
|---|---|---|
| `LISTEN` | `:8000` | address to serve on |
| `SUPERLOGICA_URL` | `https://kpgadministrado.superlogica.net` | condo site |
| `SUPERLOGICA_SSO_HOST` | `sso.superlogica.com` | Keycloak host |
| `SUPERLOGICA_SESSION_PATH` | `superlogica-session.json` | the session file |
| `OTP_RELAY_URL` / `OTP_RELAY_TOKEN` / `SUPERLOGICA_EMAIL` | — | only for the unused emailed-code path; see `OTP-RELAY.md` |

## Seeding

One hand-made login is needed to start from, and another if the Keycloak SSO
session ever expires.

1. Log in to the condo site in a browser, the normal way (email + emailed
   code).
2. Export the cookies for **both** `kpgadministrado.superlogica.net` and
   `sso.superlogica.com` as a JSON list in the shape above.
   The easiest export is **Copy as cURL**: with DevTools on the Network tab,
   load `https://sso.superlogica.com/auth/realms/superlogica-cloud/account/`,
   right-click the document request and copy it. The Keycloak cookies are
   `HttpOnly`, so no console snippet can read them.

3. Either paste the cookies into the dashboard ("Condomínio" box), which is
   the normal route, or hand the file to the tool:

   ```sh
   go run ./cmd/superlogica-login -seed curl.txt     # or a JSON cookie list
   ```

   It keeps the `sso.superlogica.com` cookies and writes them to the session
   file. A cURL aimed at the condo host is refused by name, since those
   cookies are the short-lived ones.

Whatever is pasted in the dashboard must cover **both** hosts. Condo cookies
alone give a session that works until it expires and then can't renew.

When the Keycloak session is gone too, renewal fails with *"keycloak SSO
session is gone: log in by hand and paste the session in the dashboard"*, and
pombo tells the group.

## The emailed code

Not used any more: no form is submitted, so no code is ever sent. The relay
that fetched it still exists and is now dead weight —
[OTP-RELAY.md](OTP-RELAY.md) covers what it is and how to remove it.

## How to validate

1. Seed a session (above), run the tool, then:

   ```sh
   go run ./cmd/superlogica-check -login-url http://localhost:8000/login
   ```

   Expect `OK: session is logged in`.
2. Test a real booking. Without `-keep` it cancels straight away; with it the
   booking is left for you to cancel. Use an open date, tomorrow to 30 days
   out:

   ```sh
   go run ./cmd/superlogica-check -login-url http://localhost:8000/login -book 788 -date 2026-12-20
   ```

3. Deploy pombo, make sure the saved session covers both hosts, and watch for
   `superlogica session renewed` in its log.

Unit tests cover the renewal and the expired-SSO path against a fake site
(`internal/superlogica`), and seeding and cURL parsing
(`cmd/superlogica-login`).
