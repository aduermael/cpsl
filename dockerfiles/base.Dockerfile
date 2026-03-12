FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        git tree ca-certificates ripgrep \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
