# coruja

The owl that reads every new paperless document and tells the family what it
is. Paperless calls it when a document is added; coruja waits for paperless'
own AI to finish with it, then:

1. asks Gemma for a title and a one-line summary (from the OCR text, plus the
   photo itself for JPEG/PNG documents and the first photo of a PDF made of
   photos, whose OCR is often garbage),
2. stores the summary in the custom field `Resumo`, and the title too if the
   document still has its file name as title (a title someone chose is kept),
3. clears the owner of the document's tags, correspondent and document type,
   so everyone sees them (paperless' AI creates them owned by the uploader,
   e.g. pombo),
4. calls [pombo](../pombo)'s `POST /notify`, which posts in the documents
   group:

   > 📄 *CNH do André*
   > Carteira de motorista, categoria AB, válida até 2031.
   > Enviado por Luana · https://docs.local.roggeri.com.br/documents/8/details

A document that already has a `Resumo` is skipped, so a repeated webhook is
harmless.

## Paperless setup

- Custom fields `Resumo` (long text) and `Enviado por` (text, filled by pombo).
- A user for coruja with: view/change document, view custom field, view
  paperless task, view/change tag, correspondent and document type.
- A workflow: trigger *Document Added*, action *Webhook*:
  - URL `http://coruja.coruja.svc:8080/webhook`
  - body `{"doc_id": "{{ doc_id }}"}`, sent as JSON
  - header `Authorization: Bearer <WEBHOOK_TOKEN>`

## Configuration

| Variable | Default | |
|---|---|---|
| `PAPERLESS_URL` | — | API base, e.g. `http://paperless.paperless.svc:8000` |
| `PAPERLESS_TOKEN` | — | coruja's paperless API token |
| `PAPERLESS_PUBLIC_URL` | `PAPERLESS_URL` | Paperless as people open it, for links |
| `LLM_URL` | — | OpenAI-compatible API with image input (llama.cpp) |
| `LLM_MODEL` | — | Model name, exactly as the server lists it |
| `WEBHOOK_TOKEN` | — | Bearer token paperless' webhook must send |
| `NOTIFY_URL` | — | pombo's notify endpoint, e.g. `http://pombo.pombo.svc:8080/notify`. Unset: no announcements. |
| `NOTIFY_TOKEN` | — | Bearer token for pombo |
| `LISTEN` | `:8080` | |

## Develop

```sh
make test
```

## Release

Every push to `main` that touches `apps/coruja` builds
`ghcr.io/andreroggeri/tiny-apps/coruja:sha-<commit>` for amd64 and arm64. The
chart lives in the infrastructure repo (`k3s/charts/coruja`).

## Corners cut

- The queue is in memory. A restart while a document waits for paperless'
  AI (2–3 minutes) loses that document's summary and announcement; re-run
  the workflow on it in paperless to redo it.
- Only the first photo of a PDF is sent; later pages go by their OCR text.
