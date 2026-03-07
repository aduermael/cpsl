---
name: create-plan
description: Use when the user wants to plan a task, feature, or change. Creates a structured plan with phases and checkmarked subtasks.
---

# Create a Plan

## Plan Format

1. Plans live at `plans/<plan-name>-<date>.md`
2. Structure:

	- Clear phases (numbered: 1, 2, 3...)
	- Subtasks within each phase as checkmarks: `- [ ]` (pending) or `- [x]` (done)

	**Important:** ALL phases must use checkmarks for their tasks. Each checkmark corresponds to a single commit. Phase tasks are prefixed with parent phase number and letters (a, b, c, etc.)

	Example:

	```markdown
	## Phase 1: One Phase
	- [x] 1a: Some task
	- [x] 1b: Another task
	- [ ] 1c: One more task

	## Phase 2: Another Phase
	- [ ] 2a: Some task
	- [ ] 2b: Another task
	- [ ] 2c: One more task
	```

3. Check previous plans in `plans/` and recent commits to see if the new plan builds on previous work.

4. Parallelism notation — only use when clearly beneficial:

	```markdown
	**Parallel Phases: 2,3**

	## Phase 2: Another Phase
	- [ ] 2a: Some task

	**Parallel Tasks: 2b, 2c**

	- [ ] 2b: Another task
	- [ ] 2c: One more task

	## Phase 3: One More Phase
	- [ ] 3a: Some task
	```

	Add blank lines around annotations for proper markdown rendering.

## Content Guidelines

Plans specify **what** to do, not **how** it will be executed (commits, tests, etc. are handled by `execute-plan`).

A good plan includes:
- **Codebase context** — which existing files are involved, how they're structured
- **Contracts and interfaces** — inputs, outputs, behaviors
- **Failure modes** — what can go wrong
- **Open questions** — decisions blocked on missing information
- **Tests** — every plan should include a phase (or tasks within phases) for adding or updating tests that verify the new behavior. Model-level tests using bubbletea's Update loop are the primary way to test UI behavior (window resizes, key events, state transitions)
- **Success criteria** — what tests would prove this works

A good plan does NOT contain:
- Literal implementation code with arbitrary constants
- Invented file paths or function signatures
- Details that will be discovered during implementation

## Workflow

1. Research the codebase and understand the problem
2. Ask clarifying questions if needed
3. Write the plan and commit it
4. After committing, print: `/execute-plan plans/<plan-name>` to execute it

## New Plan

Create a new plan to address this:

$ARGUMENTS
