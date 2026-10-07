FROM golang:1.25-bookworm AS go
FROM oven/bun:1.3.14
ARG OMP_VERSION=18.5.0
RUN apt-get update && apt-get install -y --no-install-recommends \
    git gh python3 python3-venv python3-pip nodejs npm ripgrep build-essential ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"
RUN bun install --global @oh-my-pi/pi-coding-agent@${OMP_VERSION} \
    && chmod o+x /root \
    && omp --version
RUN mkdir -p /workspace /results /home/sandbox \
    && chown -R 1000:1000 /workspace /results /home/sandbox
COPY runner.py /opt/blix-runner.py
COPY rpc.py /opt/rpc.py
COPY github_credentials.py gh.py documentation_policy.py prepare_repository.py /opt/
RUN mkdir -p /opt/bin && printf '#!/bin/sh\nexec python3 /opt/gh.py "$@"\n' > /opt/bin/gh && chmod +x /opt/bin/gh
ENV PATH="/opt/bin:${PATH}"
ENV HOME=/home/sandbox
USER 1000:1000
WORKDIR /workspace
ENTRYPOINT ["python3", "/opt/blix-runner.py"]
