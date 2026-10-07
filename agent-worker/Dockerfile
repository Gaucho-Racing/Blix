FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY src ./src
RUN uv sync --frozen --no-dev && /app/.venv/bin/python -m compileall -q src \
    && useradd --uid 1000 --create-home blix && chown -R blix:blix /app
USER blix
CMD ["/app/.venv/bin/blix-agent-worker"]
