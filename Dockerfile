# syntax=docker/dockerfile:1.7

FROM ghcr.io/astral-sh/uv:0.11 AS uv

FROM python:3.14-slim AS builder
COPY --from=uv /uv /uvx /usr/local/bin/
WORKDIR /app
ENV UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    UV_PYTHON_DOWNLOADS=never

COPY pyproject.toml uv.lock ./
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-install-project --no-dev

# .git/ is excluded from the build context, so hatch-vcs can't derive the
# version at build time. Override via setuptools-scm's global fallback env
# var (the per-package `_FOR_<NAME>` form gets dropped by uv's build
# isolation — unknown why; the global form is fine here because the
# container only builds one package). Placed AFTER the dependency sync so
# bumping PACKAGE_VERSION only invalidates the project-install layer
# below, not the expensive dependency layer above.
ARG PACKAGE_VERSION=0.0.0
ENV SETUPTOOLS_SCM_PRETEND_VERSION=${PACKAGE_VERSION}

COPY src/ ./src/
# hatchling reads `readme = "README.md"` from pyproject.toml when building the
# project itself; the second `uv sync` installs the project, so the file has
# to be present in the builder stage.
COPY README.md ./
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev

FROM python:3.14-slim AS runtime
# Apply Debian security updates at build time so the image ships with current
# fixes rather than whatever shipped in the base tag on its cut date.
RUN apt-get update \
 && apt-get -y upgrade \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 1000 --home-dir /app --shell /sbin/nologin mcp \
 && mkdir -p /var/lib/google-chat-mcp \
 && chown mcp:mcp /var/lib/google-chat-mcp

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
