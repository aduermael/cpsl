---
name: devenv
description: Manage the dev container Dockerfile at .herm/Dockerfile
runs_on: container
---

Runs inside the dev container. Manage the single dev container Dockerfile at .herm/Dockerfile. The built image replaces the running container and persists across sessions. Use this to install languages, tools, compilers, and system dependencies permanently. Always read before writing.

ONE environment per project. There is exactly one Dockerfile. When adding new tools, extend it — never create a parallel one.

This is the ONLY way to install tools persistently. Ad-hoc installs via bash (apt-get, apk add, pip install, npm install -g) are ephemeral and lost on container restart.

**All Dockerfiles MUST use `FROM aduermael/herm:__HERM_VERSION__` as the base image.** The `__HERM_VERSION__` placeholder is resolved automatically at build time. This image includes git, ripgrep, tree, python3, and the herm file tools. Do NOT use other base images or hardcode version tags — builds will be rejected.

**Mandatory workflow: read -> write -> build. Never skip read.**
- read: always do this first. See what's already installed.
- write: provide the COMPLETE Dockerfile starting with `FROM aduermael/herm:__HERM_VERSION__`. Keep everything already there, add what's new.
- build: apply the new image. The running container is hot-swapped.

Build proactively. Before running code that requires tools not in the current image (__CONTAINER_IMAGE__), use devenv first. Don't wait for errors.

**Dockerfile rules that prevent build failures:**
- Always extend the herm base image. Add languages and tools on top of it via apt-get.
- Look at how official Docker images (golang, node, python) install their runtimes — replicate that approach. Download official release tarballs and extract them, or use distro packages.
- Never use curl-pipe-to-bash third-party setup scripts (NodeSource setup_lts.x, rustup.sh, etc). They are fragile and break in non-interactive build environments.
- Combine related RUN steps: `apt-get update && apt-get install -y ... && rm -rf /var/lib/apt/lists/*`. Never split update and install across layers.
- Pin specific versions for reproducibility. Set WORKDIR /workspace.

**Installing runtimes — download tarballs, not setup scripts:**

Go:
```dockerfile
ENV GOLANG_VERSION=1.22.5
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget \
    && wget -qO go.tar.gz "https://go.dev/dl/go${GOLANG_VERSION}.linux-amd64.tar.gz" \
    && tar -C /usr/local -xzf go.tar.gz && rm go.tar.gz \
    && rm -rf /var/lib/apt/lists/*
ENV PATH="/usr/local/go/bin:/root/go/bin:$PATH"
```

Node.js:
```dockerfile
ENV NODE_VERSION=22.14.0
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget xz-utils \
    && wget -qO node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
    && tar -xJf node.tar.xz -C /usr/local --strip-components=1 && rm node.tar.xz \
    && rm -rf /var/lib/apt/lists/*
```

Python (already in base image — only add pip/venv if needed):
```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*
```

**RUN layer hygiene:**
```dockerfile
# correct: update + install + clean in one RUN
RUN apt-get update \
    && apt-get install -y git curl build-essential \
    && rm -rf /var/lib/apt/lists/*

# wrong: split update and install across layers
RUN apt-get update
RUN apt-get install -y git  # may use stale package lists
```

**Common mistakes that cause build failures:**
1. **Missing `apt-get update`**: Every `apt-get install` MUST be in the same RUN as `apt-get update`. The base image has no cached package lists.
2. **Wrong package names**: Debian names differ from what you expect. Use `apt-cache search` in the running container to find the right name. Common: `build-essential` (not `gcc`+`make`), `python3-dev` (not `python-dev`), `libssl-dev` (not `openssl-dev`).
3. **Downloading from URLs that 404**: Always verify download URLs. Pin exact versions — don't use "latest" links.
4. **Forgetting `-y` flag**: `apt-get install` without `-y` waits for interactive confirmation and fails.
5. **Running `apt-get update` in a separate layer**: Package lists won't be available in subsequent layers.

**When a build fails**, read the full error output. Fix only the failing step — don't rewrite the whole Dockerfile. Common fixes:
- "Unable to locate package X" → wrong package name, or forgot `apt-get update` in same layer.
- "unknown instruction" or syntax error → typo in Dockerfile syntax.
- "executable not found" → binary isn't on PATH, add an ENV PATH line.
- Script exits non-zero → requires interactive input; use `-y`, `--yes`, `-q`.
- "404 Not Found" during download → URL is wrong or version doesn't exist.
