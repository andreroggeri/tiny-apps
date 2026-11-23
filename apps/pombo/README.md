# 🕊️ pombo

A WhatsApp bot for the house. One number sits in several family groups, and
each group is routed to a handler:

| Handler | What it does |
|---|---|
| `documents` | Files photos and PDFs in paperless-ngx (details below). |
| `reservas` | Books the condo's manicure chair (details below). |

It connects as a linked device of a spare WhatsApp number (via
[whatsmeow](https://github.com/tulir/whatsmeow)), so it's unofficial: use a
number you can afford to lose, never a personal one.

## Documents handler

- **Reactions show progress:** ⏳ when a photo arrives, ✅ once paperless has
  filed it, ❌ (plus a reply saying why) if it fails. A text message that's
  picked up as context gets 📝.
- **Photos are cropped:** each photo is cut to the page and straightened by
  [tesourinha](../tesourinha) as it arrives, so paperless gets the page, not
  the table around it. Pombo replies to each cropped photo with the crop
  ("✂️ recorte"), so a bad one is spotted right away. If it finds no page or
  is down, the photo is kept as sent, with no reply. Files (photos sent as
  documents, PDFs) are never cropped.
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
- **Who sent it:** the sender's allowlist name goes in the paperless custom
  field `Enviado por` (create it once, type text). Without the field, it's
  left out.
- **Announcements:** [coruja](../coruja) summarizes every new paperless
  document and calls `POST /notify`; pombo posts title, summary, sender and
  link in every group routed to `documents`.

## Reservas handler

- **Ask in plain Portuguese:** "unha sábado 15h às 18h", "manicure dia 5 das
  3 às 6 da tarde". The LLM (`LLM_URL`) reads every message; without it the
  handler is off. If the day, start or end is missing, the bot asks ("Das 15h
  até que horas?") and waits 15 minutes for the answer. "esquece" drops it,
  and asking what's scheduled lists saved requests.
- **Whole range:** the chair is booked in 2-hour slots (6h–22h), so every slot
  touching the range is booked: 15h–18h takes 14–16 and 16–18.
- **Partial results stay:** slots that couldn't be booked are listed with the
  site's reason; the booked ones are kept.
- **Dates not open yet:** a date opens 30 days ahead at midnight. Requests for
  later dates are saved, tried every 2 s from just before midnight for 10
  minutes, and the result is posted in the group. The
  session is checked 30 minutes before; if it's dead, the group is told.
- **Login:** the site's login (email + emailed code, behind reCAPTCHA) is not
  done by pombo. A session comes from a separate login service
  (`SUPERLOGICA_LOGIN_URL`, contract in [login/README.md](login/README.md); not
  implemented yet) or is pasted in the dashboard. Pombo checks it every 5
  minutes, which keeps it from idling out, and logs how long each one lasted.
- **Test a session:** `go run ./cmd/superlogica-check -session cookies.json`
  (add `-book 788 -date YYYY-MM-DD` to book and cancel a slot).

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
| `PAPERLESS_TOKEN` | — | API token of the paperless account documents are uploaded as. Needs add/view/delete document, add note and view custom field. |
| `CROP_URL` | — | tesourinha, e.g. `http://localhost:8081` (a container in the same pod). Unset: photos are filed as sent. |
| `LLM_URL` | — | OpenAI-compatible API with image input (e.g. llama.cpp), used to group photos and to read booking messages. Unset: no grouping, and no reservas handler. |
| `LLM_MODEL` | — | Model name, exactly as the server lists it |
| `SUPERLOGICA_URL` | — | Condo site, `https://<condo>.superlogica.net` (required) |
| `SUPERLOGICA_CONDO_ID` / `SUPERLOGICA_UNIT_ID` | — | Condo and apartment on the site (required) |
| `SUPERLOGICA_LOGIN_URL` | — | Login service that returns a session. Unset: paste sessions in the dashboard. |
| `NOTIFY_TOKEN` | — | Bearer token coruja sends to `POST /notify`. Unset: the endpoint refuses everything. |

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
