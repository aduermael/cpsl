# Coding Guidelines

## File size

- Source files: 1000 lines max. If a file grows past this, split it.
- Test files: flexible — large test files that mirror source structure are fine.

## Package-level doc comments

Every non-test `.go` file must have a doc comment before the `package` declaration
explaining the file's purpose:

```go
// render.go contains terminal rendering and display functions for the TUI,
// including message formatting, code block layout, and cursor positioning.
package main
```

Keep it to 1–3 lines. Describe what the file is responsible for, not implementation
details.

## Naming

- Unexported identifiers: `camelCase` — `promptPrefix`, `maxAttachmentBytes`
- Exported identifiers: `PascalCase` — `NewBashTool`, `AgentEvent`
- Receiver names: short, lowercase — `(a *App)`, `(t *BashTool)`, `(c *ContainerClient)`
- Loop/temp variables: single letters or short abbreviations — `i`, `n`, `cfg`, `cmd`
- Constants: follow the same exported/unexported convention — `ProviderAnthropic`, `modeChat`
- Enum-like constants: use `iota`

## Imports

Three groups separated by blank lines, each sorted alphabetically:

1. Standard library
2. Third-party packages
3. Local packages (`langdag.com/...`)

## Error handling

- Always check and return errors explicitly.
- Wrap with context using `%w`: `fmt.Errorf("load config: %w", err)`
- Use `%v` or `%s` when the caller should not unwrap: `fmt.Errorf("container %s: %s", code, msg)`
- Error messages describe the operation that failed, lowercase, no trailing punctuation.
- Return early on error — avoid deep nesting.

## Comments

- Doc comments start with the identifier name: `// Definition returns the tool definition.`
- Use full sentences ending with a period.
- Inline comments explain *why*, not *what*.
- Section headers use the decorative style: `// ─── Constants ───`

## Tests

- Table-driven tests with `t.Run` for multiple cases:
  ```go
  tests := []struct {
      name string
      in   string
      want string
  }{...}
  for _, tt := range tests {
      t.Run(tt.name, func(t *testing.T) { ... })
  }
  ```
- Use `t.Helper()` in test helpers.
- Use `t.TempDir()`, `t.Setenv()`, `t.Cleanup()` instead of manual setup/teardown.
- Use `t.Fatalf` for setup failures, `t.Errorf` for assertion failures.
- No external assertion libraries — use explicit `if got != want` checks.

## File organization

Typical file structure, top to bottom:

1. Package doc comment
2. `package main`
3. Imports
4. Constants (grouped with section headers if many)
5. Type definitions
6. Constructor functions
7. Methods
8. Helper functions
