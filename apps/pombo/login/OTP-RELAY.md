# Handoff: the otp-relay, and whether to remove it

Written 2026-11-22, when pombo stopped needing it.

## What it is

A Cloudflare Worker that hands the login service the code Superlógica emails
when you sign in.

| Piece | Where |
|---|---|
| Gmail filter on Superlógica's code emails | Gmail, the condo account |
| Forwarding to a dedicated mailbox | Gmail forwarding (verified) |
| Worker `otp-relay`, keeps the latest code 10 min | infrastructure repo, `pulumi/homelab/otp-relay` |
| Endpoint `https://<otp-relay-host>/codes/superlogica` | Cloudflare, DNS on the personal domain |
| Bearer token | `pulumi stack output otpRelayToken --show-secrets` |
| Client `waitForCode` | `cmd/superlogica-login/main.go` |

```
GET https://<otp-relay-host>/codes/superlogica?since=<epoch ms>
Authorization: Bearer <token>
  404  no email after `since` yet; poll every 2–3 s
  200  {"code":"482913","from":...,"subject":...,"receivedAt":...,"text":...}
       `code` is null when no 4–9 digit number was found; read `text` then
```

## Why it exists, and why it's now unused

The plan was: drive the login form in a browser, read the emailed code from
the relay, type it in. The relay was the hard part of that and it works.

The form turned out to be behind **reCAPTCHA Enterprise**, which a server
can't satisfy. So the form is never submitted. Instead the condo site's
**Keycloak SSO session** — which long outlives the app session — is re-used to
mint a new app session with no form at all, and therefore no emailed code.
See `login/README.md`.

Nothing in pombo calls the relay. `waitForCode` is still compiled, in
`cmd/superlogica-login` (a hand tool, not deployed), and nothing calls it.

## Should it go?

**Recommendation: yes, but not yet. Decide after the SSO renewal has run for
a few weeks.**

For removing it:

- It is dead weight, and dead infrastructure rots silently. Gmail forwarding
  in particular can lapse without anyone noticing.
- Its value is contingent on a path that is blocked anyway. Even if the SSO
  renewal stopped working tomorrow, the relay alone wouldn't rescue it: that
  path also needs a browser in the cluster *and* a passing reCAPTCHA score.
  The relay is the easy third of a plan whose other two thirds are the
  problem.
- It holds a live secret and a public endpoint for no current purpose.

For keeping it a while:

- It costs about nothing to run.
- The one real unknown — how long Keycloak's SSO session survives idle
  (`login/HANDOFF.md`) — isn't answered yet. If it turns out to be short, the
  design gets revisited, and it's nicer to still have this.

So: leave it until pombo has renewed sessions unattended for a few weeks. If
the logs show `superlogica session renewed` working and no hand seeding was
needed, remove it.

## How to remove it, when that day comes

In this repo:

1. Delete `waitForCode` and its test from `cmd/superlogica-login`.
2. Drop `OTP_RELAY_URL`, `OTP_RELAY_TOKEN` and `SUPERLOGICA_EMAIL` from that
   command's config.
3. Delete this file and the "Getting the emailed code" section of
   `login/README.md`.

In the infrastructure repo (**not checked from here — verify before deleting**):

4. Remove the `otp-relay` component under `pulumi/homelab/`, then `pulumi up`.
   Check what it owns besides the Worker: the relay's DNS record,
   any KV namespace or Durable Object holding the codes, and the
   `otpRelayToken` secret.
5. Remove `OTP_RELAY_TOKEN` from pombo's sealed secret if it was ever added.

In Gmail:

6. Delete the filter on Superlógica's code emails.
7. Remove the forwarding address a dedicated mailbox.

Then add a line to `apps/pombo/DECISIONS.md`.

## If it's kept instead

It needs an owner and a check. Gmail forwarding lapses, Worker runtimes get
deprecated, and the token has no rotation. A relay nobody tests is worse than
no relay, because it looks like a working fallback. If it stays past the
decision point above, put a yearly reminder on it to send a test email and
confirm a code comes back.
