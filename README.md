# 🕊️ pombo

A WhatsApp bot for the house. One number sits in several family groups, and
each group is routed to a handler:

| Handler | What it does |
|---|---|
| `documents` | Photos and PDFs sent to the group are filed in paperless-ngx; the bot replies with the title, correspondent and tags its AI workflow picked. |

It connects as a linked device of a spare WhatsApp number (via
[whatsmeow](https://github.com/tulir/whatsmeow)), so it's unofficial: use a
number you can afford to lose, never a personal one.

## Dashboard

One page on `:8080` to pair the number (QR code), route groups to handlers and
manage who may use the bot. Only allowlisted phone numbers are obeyed, even
inside routed groups. The dashboard has no login of its own; run it behind the
ingress SSO.

## Configuration

| Variable | Default | |
|---|---|---|
| `POMBO_DATA_DIR` | `./data` | WhatsApp session and settings (two SQLite files). Back it up. |
| `POMBO_LISTEN` | `:8080` | Dashboard address |
| `PAPERLESS_URL` | — | API base the bot calls, e.g. `http://paperless.paperless.svc:8000` |
| `PAPERLESS_PUBLIC_URL` | `PAPERLESS_URL` | Base for links sent to the chat |
| `PAPERLESS_TOKEN` | — | API token of the paperless account documents are uploaded as |

## Develop

```sh
go test ./...
POMBO_DATA_DIR=./data PAPERLESS_URL=https://docs.example PAPERLESS_TOKEN=... go run ./cmd/pombo
```

## Release

Every push to `main` builds `ghcr.io/andreroggeri/pombo:sha-<commit>` (and
`latest`) for amd64 and arm64 after the tests pass; a `vX.Y.Z` tag also
publishes `X.Y.Z`. See `.github/workflows/release.yml`.

Then set the tag in `k3s/applications/pombo/values.yaml` in the infrastructure
repo and deploy with `helmfile -l name=pombo --skip-deps sync`.

## Adding a handler

Implement `router.Handler`, register it under a name in `cmd/pombo/main.go`,
and pick it for a group in the dashboard.
