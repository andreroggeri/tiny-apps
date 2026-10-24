# Decisions

Newest first. One line per deployed version: what changed and why.

- 2026-10-24: PDFs made of photos send their first photo too, and temperature is 0, because garbled OCR made a CNH's issue date come out as its expiry date.
- 2026-10-24: summaries leave out document numbers (CPF, RG, barcodes), because they're posted in a WhatsApp group; kept Gemma 26B-A4B after E4B got dates and names wrong at the same speed.
- 2026-10-24: first version. Summaries live in paperless (field `Resumo`) so every upload gets one and they can be searched later; pombo only announces them.
