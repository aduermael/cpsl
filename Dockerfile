# Herm base image: aduermael/herm:<tag>
# All herm containers extend this image. Contains the CLI tools (edit-file,
# write-file) and essential system utilities (git, ripgrep, tree, python3).

FROM golang:1.24-bookworm AS builder
COPY tools/ /build/tools/
RUN cd /build/tools/edit-file && go build -o /out/edit-file .
RUN cd /build/tools/write-file && go build -o /out/write-file .
RUN cd /build/tools/outline && go build -o /out/outline .

FROM debian:bookworm-slim
COPY --from=builder /out/edit-file /out/write-file /out/outline /usr/local/bin/
COPY --from=builder /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"
RUN apt-get update && apt-get install -y --no-install-recommends \
        git tree ca-certificates ripgrep python3 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
