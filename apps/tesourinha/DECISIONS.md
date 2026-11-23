# Decisions

Newest first. One line per deployed version: what changed and why.

- 2026-11-23: crops and straightens photos of documents before pombo files them, because photos landed in paperless with the couch around the page. Classic OpenCV, not the vision LLM: in a test on real photos it found all 4 pages, while Gemma and Qwen got none right.
