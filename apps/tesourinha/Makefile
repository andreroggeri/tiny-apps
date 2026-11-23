IMAGE ?= tesourinha:dev
PY    := uv run --python 3.12 --with-requirements requirements.txt
PYTHONWARNINGS := ignore

.PHONY: build test fmt lint run image clean

# Nothing to compile; the image is the build.
build:
	@true

test:
	$(PY) python -m unittest discover -s tests -t . -v

fmt:
	uvx ruff@0.14.6 format tesourinha tests

# CI-safe: fails if anything is unformatted, instead of rewriting it.
lint:
	uvx ruff@0.14.6 format --check tesourinha tests
	uvx ruff@0.14.6 check tesourinha tests

run:
	$(PY) python -m tesourinha.server

image:
	docker build -t $(IMAGE) .

clean:
	@true
