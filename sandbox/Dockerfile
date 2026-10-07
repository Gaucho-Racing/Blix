FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends git openssl && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY src ./src
RUN uv sync --frozen --no-dev
CMD ["/app/.venv/bin/blix-sandbox"]
