# Decisions

Newest first. One line per deployed version: what changed and why.

- 2026-10-24: moved into the tiny-apps repo (image `ghcr.io/andreroggeri/tiny-apps/pombo`), to keep the small home apps in one place.
- 2026-10-24: late pages can join a document for 1 h, not 10 min, because the first real CNH screenshots arrived 37 min apart.
- 2026-10-24: texts go to the document they're about, and late pages replace the old document only if Gemma is ≥90% sure (yes/no token probability), because one batch text was mislabeling every document.
- 2026-10-23: reactions instead of replies, 30 s photo batches grouped by Gemma, and context added as a PDF page, because paperless' AI reads document text, not notes.
- 2026-10-23: CI publishes public multi-arch images to GHCR, so k3s can pull them without a secret.
- 2026-10-23: WhatsApp bot (whatsmeow, spare number) that files group photos and PDFs in paperless.
