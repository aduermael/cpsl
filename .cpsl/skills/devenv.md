---
name: Dev Environment
description: Guidelines for setting up and extending the single project dev container
---

There is ONE dev environment per project: .cpsl/Dockerfile. Every tool installation goes here. Never create a second Dockerfile for a different purpose.

The default base image is `debian:bookworm-slim` with exploration tools pre-installed: git, ripgrep (`rg`), tree, GNU grep, and findutils. On first startup, if no `.cpsl/Dockerfile` exists, the embedded base template is written and built automatically — so new projects get these tools out of the box. If you need Alpine, create your own `.cpsl/Dockerfile` with `alpine:3` as the base.

## The invariant

The Dockerfile grows over time. It never shrinks (unless you're removing something intentionally). If the project needs Go today and TypeScript tomorrow, tomorrow's Dockerfile has both. The running container always reflects the latest build.

## Workflow

Every time you need to change the environment:

1. `devenv read` — always start here. Note the current base image and what's already installed.
2. `devenv write` — write the complete new Dockerfile. Include everything from step 1 plus your additions.
3. `devenv build` — build and hot-swap. If it fails, read the error and fix the specific failing step.

Never skip step 1. The most common mistake is writing a Dockerfile without reading first, then accidentally removing tools that were already there.

## Choosing a base image

Always start from a clean base: `debian:bookworm-slim` or `alpine:3`. Install languages and tools yourself via the distro package manager. This gives you full control over versions and makes it easy to combine multiple runtimes without conflicts.

Avoid:
- Official language images (`golang:`, `node:`, `python:`) as base — they work for single-language projects but conflict when combining runtimes, and you lose control over versions
- `:latest` tags — unpredictable, breaks cached builds
- Mixing package managers (apt-get on alpine, apk on debian)
- Third-party curl-pipe-to-bash setup scripts (NodeSource setup_lts.x, rustup.sh, etc.) — they're fragile and break in non-interactive builds

## How to install runtimes

Look at how official Docker images install their runtimes. The pattern is usually: download the official release tarball, extract it, set PATH. Examples:

**Go** (from the official golang image approach):
```dockerfile
ENV GOLANG_VERSION=1.22.5
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget \
    && wget -qO go.tar.gz "https://go.dev/dl/go${GOLANG_VERSION}.linux-amd64.tar.gz" \
    && tar -C /usr/local -xzf go.tar.gz && rm go.tar.gz \
    && rm -rf /var/lib/apt/lists/*
ENV PATH="/usr/local/go/bin:/root/go/bin:$PATH"
```

**Node.js** (from the official node image approach):
```dockerfile
ENV NODE_VERSION=22.14.0
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget xz-utils \
    && wget -qO node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
    && tar -xJf node.tar.xz -C /usr/local --strip-components=1 && rm node.tar.xz \
    && rm -rf /var/lib/apt/lists/*
```

**Python** (distro package — simpler when exact version doesn't matter):
```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*
```

## Multi-language example

Go + TypeScript on a clean base:

```dockerfile
FROM debian:bookworm-slim
WORKDIR /workspace

# Go
ENV GOLANG_VERSION=1.22.5
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates wget git xz-utils \
    && wget -qO go.tar.gz "https://go.dev/dl/go${GOLANG_VERSION}.linux-amd64.tar.gz" \
    && tar -C /usr/local -xzf go.tar.gz && rm go.tar.gz \
    && rm -rf /var/lib/apt/lists/*
ENV PATH="/usr/local/go/bin:/root/go/bin:$PATH"

# Node.js + TypeScript
ENV NODE_VERSION=22.14.0
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget xz-utils \
    && wget -qO node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
    && tar -xJf node.tar.xz -C /usr/local --strip-components=1 && rm node.tar.xz \
    && npm install -g typescript \
    && rm -rf /var/lib/apt/lists/*
```

## RUN layer hygiene

```dockerfile
# correct: update + install + clean in one RUN
RUN apt-get update \
    && apt-get install -y git curl build-essential \
    && rm -rf /var/lib/apt/lists/*

# wrong: split update and install across layers
RUN apt-get update
RUN apt-get install -y git  # may use stale package lists
```

## When a build fails

Read the full error output. Docker reports the exact failing step and the error message. Fix only that step — don't rewrite the whole Dockerfile. Common fixes:

- "Unable to locate package X" → wrong package name, or forgot `apt-get update` in same layer
- "unknown instruction" or syntax error → typo in Dockerfile syntax
- "executable not found" → the binary isn't on PATH, add an ENV PATH line
- Script exits non-zero → the script requires interactive input; use flags like `-y`, `--yes`, `-q`
- NodeSource setup error → remove it, use `FROM node:22` as base instead
