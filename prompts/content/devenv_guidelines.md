## Dockerfile guidelines

**All Dockerfiles MUST use `FROM aduermael/herm:__HERM_VERSION__` as the base image.** The base image includes git, ripgrep, tree, python3, and the herm file tools. Do NOT use other base images or hardcode version tags.

**Dockerfile rules that prevent build failures:**
- Download official release tarballs rather than curl-pipe-to-bash setup scripts (NodeSource, rustup.sh, etc).
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

**Common mistakes that cause build failures:**
1. Missing `apt-get update` in same RUN layer as install — the base image has no cached package lists.
2. Wrong Debian package names — use `apt-cache search` in the running container. Common: build-essential (not gcc+make), python3-dev (not python-dev), libssl-dev (not openssl-dev).
3. Download URLs that 404 — always verify and pin exact versions.
4. Missing -y flag on apt-get install.
5. curl-pipe-to-bash setup scripts — use tarballs instead.

**When a build fails**, read the full error. Fix only the failing step — don't rewrite the whole Dockerfile.
