package main

import _ "embed"

// BaseDockerfile is the default Dockerfile template for new projects.
// Debian bookworm-slim with essential exploration tools: git, ripgrep, tree.
//
//go:embed dockerfiles/base.Dockerfile
var BaseDockerfile string
