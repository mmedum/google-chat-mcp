# syntax=docker/dockerfile:1.7

FROM ghcr.io/astral-sh/uv:0.11 AS uv

FROM python:3.12-slim AS builder
COPY --from=uv /uv /uvx /usr/local/bin/
WORKDIR /app
ENV UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    UV_PYTHON_DOWNLOADS=never

COPY pyproject.toml uv.lock ./
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-install-project --no-dev

COPY src/ ./src/
COPY migrations/ ./migrations/
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev

FROM python:3.12-slim AS runtime
RUN useradd --system --uid 1000 --home-dir /app --shell /sbin/nologin mcp

WORKDIR /app
COPY --from=builder --chown=mcp:mcp /app /app
ENV PATH="/app/.venv/bin:$PATH" \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

USER mcp
EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=10s \
  CMD ["python", "-c", "import urllib.request,sys;sys.exit(0 if urllib.request.urlopen('http://localhost:8000/healthz',timeout=3).status==200 else 1)"]

CMD ["python", "-m", "src.server"]
