# 🕊️ pombo

A WhatsApp bot for the house. One number sits in several family groups, and
each group is routed to a handler:

| Handler | What it does |
|---|---|
| `documents` | Files photos and PDFs in paperless-ngx (details below). |

It connects as a linked device of a spare WhatsApp number (via
[whatsmeow](https://github.com/tulir/whatsmeow)), so it's unofficial: use a
number you can afford to lose, never a personal one.

## Documents handler

- **Reactions show progress:** ⏳ when a photo arrives, ✅ once paperless has
  filed it, ❌ (plus a reply saying why) if it fails. A text message that's
  picked up as context gets 📝.
- **Photos are batched:** photos from the same person arriving within 30 s of
  each other are held, then a vision LLM groups them into documents, so a
  contract photographed page by page becomes one multi-page PDF. Without an
  LLM (or if it fails), each photo becomes its own document.
- **Late pages:** for an hour after filing, a new photo is compared with the
  sender's last few documents (by their OCR text, or their last page when it
  has little text). If the LLM is at least 90% sure it's another page of one,
  that document is replaced by one with all the pages; the old one goes to
  paperless' trash. This needs the delete-document permission.
- **Context:** a caption or a text message ("carteira de vacinação do bebê")
  is added as a final page of the PDF it's about, so paperless' AI
  suggestions and search read it. The LLM decides which document each text
  is about; without it, a text applies to the photos sent right after it. A
  text sent with no photo is kept for 10 minutes for the next one. PDF files
  are uploaded unchanged, with the context as a note.

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
| `PAPERLESS_TOKEN` | — | API token of the paperless account documents are uploaded as. Needs add/view document and add note. |
| `LLM_URL` | — | OpenAI-compatible API with image input (e.g. llama.cpp), used to group photos. Unset: no grouping. |
| `LLM_MODEL` | — | Model name, exactly as the server lists it |

## Develop

```sh
make test
POMBO_DATA_DIR=./data PAPERLESS_URL=https://docs.example PAPERLESS_TOKEN=... go run ./cmd/pombo
```

## Release

Every push to `main` that touches `apps/pombo` builds
`ghcr.io/andreroggeri/tiny-apps/pombo:sha-<commit>` (and `latest`) for amd64
and arm64 after the tests pass. See `.github/workflows/pombo.yml` at the repo
root.

Then set the tag in `k3s/applications/pombo/values.yaml` in the infrastructure
repo and deploy with `helmfile -l name=pombo --skip-deps sync`.

## Adding a handler

Implement `router.Handler`, register it under a name in `cmd/pombo/main.go`,
and pick it for a group in the dashboard.
