---
name: Dev Environment
description: Guidelines for setting up and customizing the development container
---

- The devenv tool is your ONLY way to install tools, languages, and dependencies persistently. Ad-hoc installs via bash (apt-get, apk add, pip install) are lost when the container restarts.
- Always use devenv to improve the image, not the running container. Think of it like writing infrastructure-as-code.
- Read the current devenv state first — check if a .cpsl/*.Dockerfile already exists.
- If the project has a Dockerfile in the root, consider using it as a starting base.
- Check for dependency files (go.mod, package.json, requirements.txt, Gemfile, etc.) and include their install step in the Dockerfile.
- After writing the Dockerfile, build it to apply changes — this hot-swaps the running container.
- Keep the Dockerfile minimal — only include what's needed for the project.
- If the user asks to "set up the environment", "install X", or you detect missing tools, this is your cue to use devenv.
