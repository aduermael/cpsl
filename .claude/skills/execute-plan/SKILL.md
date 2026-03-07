---
name: execute-plan
description: Use when a plan exists and needs to be executed or continued. Reads the plan, works task by task, commits after each, and pauses at phase boundaries.
---

# Execute or Continue Plan

## Plan Reference

Start or continue executing this plan: $ARGUMENTS

## Execution Workflow

1. **Read the plan** — find the next incomplete phase (first `- [ ]`)
2. **Work task by task** — complete one subtask at a time, commit after each
3. **Update the plan** — mark tasks `- [x]` in the same commit as the code change
4. **Run checks at phase end** — `go test ./...`
5. **Pause at phase boundaries** — stop at the end of each phase for user review unless specified otherwise

## Task Execution

For each task:

1. Implement the change
2. Update the plan: change `- [ ]` to `- [x]`
3. Commit both code changes AND plan update together
4. Move to the next task

## Parallel Tasks

If the plan contains parallel notation:

```markdown
**Parallel Tasks: 2b, 2c**
- [ ] 2b: Task one
- [ ] 2c: Task two
```

Execute these simultaneously using multiple agents, then commit each separately.

For parallel phases (`**Parallel Phases: 2,3**`), spawn agents for each phase and work them concurrently.

## Commit Format

- Plain text only — no title/body separation
- Maximum 5 lines
- No "Co-Authored-By:" or "Generated with" lines

**Naming convention** — phase number + letter:

```
1a: Add Cell and Axis structs
1b: Add Workbook and Sheet structs
2a: Implement text format parser
```

Every commit includes the checkmark update to the plan file.

## Phase Completion

At the end of each phase:

1. Run tests – `go test ./...`
2. Fix any failures before moving on
3. **Stop and report** — summarize what was completed, what's next

## Handling Blockers

1. **Don't skip the task** — stop and address it
2. **Document the blocker** in your response
3. **Ask for guidance** if resolution is unclear
4. **Update the plan** if scope changes are needed

## Progress Reports

When pausing or completing, report:

- **Completed**: Tasks finished this session
- **Current status**: Where execution stopped
- **Next up**: What comes next
- **Blockers**: Any issues encountered
- **Plan location**: Path to the plan file
