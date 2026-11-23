# Wheels for the target platform are downloaded on the build machine's
# platform, so the arm64 image needs no emulation.
FROM --platform=$BUILDPLATFORM python:3.12-slim AS deps
ARG TARGETARCH
COPY requirements.txt /tmp/
RUN case "$TARGETARCH" in \
      amd64) plat=manylinux_2_28_x86_64 ;; \
      arm64) plat=manylinux_2_28_aarch64 ;; \
      *) echo "unsupported arch $TARGETARCH" >&2; exit 1 ;; \
    esac && \
    pip install --no-cache-dir --target /deps --only-binary=:all: \
      --platform "$plat" --python-version 3.12 --implementation cp \
      -r /tmp/requirements.txt

FROM python:3.12-slim
ENV PYTHONPATH=/deps:/app PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
COPY --from=deps /deps /deps
COPY tesourinha /app/tesourinha
EXPOSE 8081
USER 65532:65532
ENTRYPOINT ["python", "-m", "tesourinha.server"]
