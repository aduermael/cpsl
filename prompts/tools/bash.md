---
name: bash
description: Run a shell command in the dev container
runs_on: container
---

Runs inside the dev container (image: __CONTAINER_IMAGE__, project mounted at /workspace). Use for: running builds, tests, installs, and commands that aren't file reads. Output is truncated to 80 lines / 12KB (head+tail).

The base container is minimal — it may lack compilers, runtimes, and dev tools. Before running project code, check if required tools are installed (e.g. 'which go' or 'python3 --version'). If missing, use devenv to build a proper image — don't ad-hoc install or try to run code that will fail.

Do NOT install tools/runtimes via bash (e.g. apt-get install, apk add). Those installs are ephemeral and lost on container restart. Use devenv instead to persist them in the image.

Run tests after changes.
