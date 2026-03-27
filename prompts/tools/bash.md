---
name: bash
description: Run a shell command in the dev container
runs_on: container
---

Runs inside the dev container (image: __CONTAINER_IMAGE__, project mounted at __WORK_DIR__). Output is truncated to 80 lines / 12KB (head+tail).

Prefer dedicated tools for reading, searching, and editing files. Use bash for commands without a dedicated tool equivalent: running builds, tests, mkdir, mv, cp, chmod, curl, etc.

Do NOT install tools/runtimes via bash (e.g. apt-get install, apk add). Those installs are ephemeral and lost on container restart. Use devenv instead to persist them in the image.
