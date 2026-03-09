---
name: Dev Environment
description: Guidelines for setting up and customizing the development container
---

- When the user wants to install tools, languages, or system dependencies, use the devenv tool to create a persistent Dockerfile rather than installing via ad-hoc bash commands.
- Always read the current devenv state first — check if a .cpsl/Dockerfile already exists.
- If the project has a Dockerfile in the root, consider using it as a starting base for .cpsl/Dockerfile.
- After writing the Dockerfile, build it to apply changes to the running container.
- Keep the Dockerfile minimal — only include what's needed for the project.
- If the user asks to "set up the environment" or "install X", this is your cue to use the devenv tool.
