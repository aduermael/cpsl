---
name: Dev Environment
description: Guidelines for setting up and extending the single project dev container
---

There is ONE dev environment per project: .herm/Dockerfile. Every tool installation goes here. Never create a second Dockerfile for a different purpose.

The default base image is `aduermael/herm:0.1` — a Debian bookworm-slim image with essential tools pre-installed: git, ripgrep (`rg`), tree, python3, and the herm file tools (edit-file, write-file). On first startup, if no `.herm/Dockerfile` exists, the base image is pulled directly — no build step needed. New projects get all tools out of the box.

**All custom Dockerfiles MUST use `FROM aduermael/herm:0.1` as the base.** This ensures the file editing tools and core utilities are always available. Builds will be rejected if a different base image is used.

## The invariant

The Dockerfile grows over time. It never shrinks (unless you're removing something intentionally). If the project needs Go today and TypeScript tomorrow, tomorrow's Dockerfile has both. The running container always reflects the latest build.

## Workflow

Every time you need to change the environment:

1. `devenv read` — always start here. Note what's already installed.
2. `devenv write` — write the complete new Dockerfile. Include everything from step 1 plus your additions. Must use `FROM aduermael/herm:0.1`.
3. `devenv build` — build and hot-swap. If it fails, read the error and fix the specific failing step.

Never skip step 1. The most common mistake is writing a Dockerfile without reading first, then accidentally removing tools that were already there.

## Base image

Always extend `aduermael/herm:0.1`. This image includes:
- Debian bookworm-slim
- git, tree, ca-certificates, ripgrep, python3
- edit-file, write-file (herm CLI tools)

Add languages and tools on top of it:

```dockerfile
FROM aduermael/herm:0.1
WORKDIR /workspace

# Add Go
ENV GOLANG_VERSION=1.22.5
RUN apt-get update && apt-get install -y --no-install-recommends wget \
    && wget -qO go.tar.gz "https://go.dev/dl/go${GOLANG_VERSION}.linux-amd64.tar.gz" \
    && tar -C /usr/local -xzf go.tar.gz && rm go.tar.gz \
    && rm -rf /var/lib/apt/lists/*
ENV PATH="/usr/local/go/bin:/root/go/bin:$PATH"
```

Do NOT use other base images (debian:bookworm-slim, alpine:3, node:22, golang:1.22, etc.). The devenv tool will reject them.

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

**Python** (already in the base image — only add pip/venv if needed):
```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*
```

## Multi-language example

Go + TypeScript on the herm base:

```dockerfile
FROM aduermael/herm:0.1
WORKDIR /workspace

# Go
ENV GOLANG_VERSION=1.22.5
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates wget xz-utils \
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

## Common mistakes that cause build failures

1. **Missing `apt-get update`**: Every `apt-get install` MUST be in the same RUN as `apt-get update`. The base image has no cached package lists.
2. **Wrong package names**: Debian package names are often different from what you expect. Use `apt-cache search` in the running container to find the right name before writing the Dockerfile. Common ones: `build-essential` (not `gcc`+`make`), `python3-dev` (not `python-dev`), `libssl-dev` (not `openssl-dev`).
3. **Downloading from URLs that 404**: Always verify download URLs. Pin exact versions — don't use "latest" download links.
4. **Forgetting `-y` flag**: `apt-get install` without `-y` waits for interactive confirmation and fails in builds.
5. **Running `apt-get update` in a separate layer**: Package lists won't be available in subsequent layers.

## When a build fails

Read the full error output. Docker reports the exact failing step and the error message. Fix only that step — don't rewrite the whole Dockerfile. Common fixes:

- "Unable to locate package X" → wrong package name, or forgot `apt-get update` in same layer. Run `apt-cache search <keyword>` in the container to find the right name.
- "unknown instruction" or syntax error → typo in Dockerfile syntax
- "executable not found" → the binary isn't on PATH, add an ENV PATH line
- Script exits non-zero → the script requires interactive input; use flags like `-y`, `--yes`, `-q`
- "404 Not Found" during download → URL is wrong or version doesn't exist. Check the official download page.
