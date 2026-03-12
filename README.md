# cpsl

A terminal-based chat interface for LLM agents with Docker container support, git worktree management, and a custom raw-terminal TUI engine.

## Features

- Interactive chat with LLM agents via [langdag](https://langdag.com)
- Docker container integration for sandboxed code execution
- Git worktree management
- Markdown rendering in the terminal
- Configurable models, skills, and system prompts
- Conversation history and scratchpad

## Build

Requires Go 1.25+.

```sh
go build -o cpsl ./cmd/cpsl
```

Additional commands:

```sh
go build ./cmd/simple-chat   # minimal chat client
go build ./cmd/debug          # debug utilities
```

## Run

```sh
./cpsl
```

## Test

```sh
go test ./...
```

## Project Structure

```
cmd/cpsl/         Main application source (package main)
cmd/cpsl/prompts/ System prompt templates (embedded)
cmd/cpsl/dockerfiles/ Dockerfiles for container support (embedded)
cmd/simple-chat/  Minimal chat client
cmd/debug/        Debug utilities
plans/            Project planning docs
```

## License

[MIT](LICENSE)
