# ✂️ tesourinha

Finds the page in a photo of a document and returns it cropped and
straightened, so paperless gets the page and not the couch around it.
pombo calls it for every photo before filing it.

## API

`POST /crop` with a JPEG or PNG body:

| Status | Body | Meaning |
|---|---|---|
| 200 | JPEG | The page, cropped and straightened. |
| 204 | — | No page found, or the photo is already cropped (the page fills over 90% of it). Keep the original. |
| 400 | text | Not an image. |

`GET /healthz` returns 200.

## How it finds the page

OpenCV, no model:

1. **Edges:** the biggest 4-sided outline. Works on plain backgrounds, even
   a white table.
2. **Brightness:** on busy backgrounds (a couch) edges find nothing, or an
   outline that takes in some background. Then the page is the biggest
   bright patch.

A spike compared this with asking the vision LLM for the corners (Gemma and
Qwen on the llama.cpp server) and with DocAligner, a corner model. On real
photos from paperless, this found all 4 pages; the others got 0–1 right.

## Configuration

| Variable | Default | |
|---|---|---|
| `TESOURINHA_LISTEN` | `:8081` | Address. pombo runs on `:8080` in the same pod. |

## Develop

Needs [uv](https://docs.astral.sh/uv/).

```sh
make test
make run      # listens on :8081
curl --data-binary @photo.jpg localhost:8081/crop -o page.jpg
```

## Deploy

CI publishes `ghcr.io/andreroggeri/tiny-apps/tesourinha:sha-<commit>`. It
runs as a second container in the pombo pod: set `tesourinha.image.tag` in
`k3s/applications/pombo/values.yaml` in the infrastructure repo.
