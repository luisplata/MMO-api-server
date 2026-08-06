# Skill Registry

**Delegator use only.** Any agent that launches sub-agents reads this registry to resolve compact rules, then injects them directly into sub-agent prompts. Sub-agents do NOT read this registry or individual SKILL.md files.

See `_shared/skill-resolver.md` for the full resolution protocol.

## User Skills

| Trigger | Skill | Path |
|---------|-------|------|
| When creating a pull request, opening a PR, or preparing changes for review | branch-pr | C:\Users\luis_\.config\opencode\skills\branch-pr\SKILL.md |
| when a PR would exceed 400 changed lines, when planning chained PRs, stacked PRs, or reviewable slices | gentle-ai-chained-pr | C:\Users\luis_\.config\opencode\skills\chained-pr\SKILL.md |
| when writing guides, READMEs, RFCs, onboarding docs, architecture docs, or review-facing documentation | cognitive-doc-design | C:\Users\luis_\.config\opencode\skills\cognitive-doc-design\SKILL.md |
| when drafting or posting feedback, review comments, maintainer replies, Slack messages, or GitHub comments | comment-writer | C:\Users\luis_\.config\opencode\skills\comment-writer\SKILL.md |
| When writing Go tests, using teatest, or adding test coverage | go-testing | C:\Users\luis_\.config\opencode\skills\go-testing\SKILL.md |
| When creating a GitHub issue, reporting a bug, or requesting a feature | issue-creation | C:\Users\luis_\.config\opencode\skills\issue-creation\SKILL.md |
| When user says "judgment day", "judgment-day", "review adversarial", "dual review", "doble review", "juzgar", "que lo juzguen" | judgment-day | C:\Users\luis_\.config\opencode\skills\judgment-day\SKILL.md |
| When user asks to create a new skill, add agent instructions, or document patterns for AI | skill-creator | C:\Users\luis_\.config\opencode\skills\skill-creator\SKILL.md |
| when implementing a change, preparing commits, splitting PRs, or planning chained or stacked PRs | work-unit-commits | C:\Users\luis_\.config\opencode\skills\work-unit-commits\SKILL.md |

## Compact Rules

Pre-digested rules per skill. Delegators copy matching blocks into sub-agent prompts as `## Project Standards (auto-resolved)`.

### branch-pr
- Every PR MUST link an issue with `status:approved` label — blank PRs are blocked by GitHub Actions
- Branch names MUST match `^(feat|fix|chore|docs|style|refactor|perf|test|build|ci|revert)\/[a-z0-9._-]+$`
- PR body MUST use template: `Closes #N`, exactly ONE `type:*` label, Changes table, Test Plan, Contributor Checklist
- Conventional commits required: `type(scope): description`; no `Co-Authored-By` trailers
- Type-to-label: feat→type:feature, fix→type:bug, docs→type:docs, refactor→type:refactor, chore/style/test/build/ci→type:chore, perf→type:feature, feat!/fix!→type:breaking-change
- Run shellcheck on modified scripts before pushing

### gentle-ai-chained-pr
- MUST split when PR exceeds 400 changed lines (additions+deletions) unless maintainer-approved `size:exception`
- Every chained PR: one deliverable work unit, CI green, clear rollback, verification included, reviewable alone
- Each PR MUST state Chain Context: position, base, depends on, follow-up, review budget, starts at, ends with
- Every child PR MUST include a dependency diagram marking the current PR with 📍
- For >2 PRs, create a draft tracker PR (map only, `no-merge` until chain complete)
- Feature Branch Chain: all children target the feature/tracker branch, NEVER main
- Stacked: each PR targets the previous branch; after merge, rebase + retarget to main
- Honor SDD `delivery_strategy`: ask-on-risk → ask; auto-chain → slice automatically; single-pr → require size:exception; exception-ok → record accepted exception

### cognitive-doc-design
- Lead with the answer: decision/action/outcome first, context after
- Progressive disclosure: happy path → details → edge cases → references
- Chunk related info into small sections; keep flat lists short
- Signpost with headings, labels, callouts, summaries
- Prefer recognition over recall: tables, checklists, examples, templates over prose
- Design for review empathy: state review order, out-of-scope items, and prev/next PR links in chains

### comment-writer
- Start with the actionable point; do not recap the whole PR first
- Warm and direct, 1-3 short paragraphs or a tight bullet list
- Explain WHY when requesting a change; avoid pile-ons (highest-value issue only)
- Match thread language; Spanish → Rioplatense/voseo (podés, tenés, fijate)
- No em dashes — use commas, periods, parentheses

### go-testing
- Table-driven tests for pure functions; test success AND error cases
- Tests in `_test.go` files beside the code; same package
- Bubbletea: test Model.Update() state transitions directly with tea.KeyMsg
- Teatest: `teatest.NewTestModel(t, m)` for full interactive flows
- Golden file testing for rendered output; `-update` flag regenerates; files in `testdata/`
- Mock side effects via interfaces; file ops use `t.TempDir()`; system calls use interface+mock
- Commands: `go test ./...`, `go test -v ./...`, `go test -cover ./...`, `go test -short ./...`

### issue-creation
- Blank issues disabled — MUST use template (bug_report.yml or feature_request.yml)
- New issues auto-label `status:needs-review`; maintainer MUST add `status:approved` before any PR
- Search existing issues for duplicates BEFORE creating
- Questions go to Discussions, not issues
- Bug: pre-flight checks + steps to reproduce + expected/actual + OS/agent/shell. Feature: problem + proposed solution + affected area

### judgment-day
- Launch TWO blind judge sub-agents in parallel (delegate async, never sequential); identical prompts
- Orchestrator NEVER reviews code itself — only launches, reads, synthesizes
- Classify WARNINGs: "Can a normal user trigger this?" YES→real (fix), NO→theoretical (report as INFO, do not fix)
- Confirmed = found by BOTH judges; suspect = one judge only (triage, don't auto-fix)
- Round 1: ASK user before fixing; Round 2+: re-judge only on confirmed CRITICALs
- APPROVED = 0 CRITICAL + 0 real WARNING; after 2 fix iterations, ASK user before continuing
- MUST NOT push/commit/summarize until every JD reaches APPROVED or ESCALATED

### skill-creator
- Structure: `skills/{name}/SKILL.md` (+ optional `assets/` templates, `references/` local docs)
- Frontmatter required: name (lowercase, hyphens), description with `Trigger:`, license `Apache-2.0`, metadata.author `gentleman-programming`, metadata.version
- `references/` MUST point to LOCAL files, never web URLs
- DO: start with most critical patterns, tables for decisions, minimal examples, Commands section
- DON'T: Keywords section, duplicate docs content, lengthy explanations, troubleshooting, web URLs in references
- After creating, register in AGENTS.md table
- Don't create skills for one-off tasks or existing docs

### work-unit-commits
- Commit by work unit (deliverable behavior/fix/migration/docs), NEVER by file type (models, then services, then tests)
- Tests and docs belong in the SAME commit as the code they verify
- Each commit: one clear purpose; repo still makes sense after applying only it; rollback doesn't revert unrelated work
- Commit message explains the OUTCOME, not the file list
- Conventional commits; use `git diff --stat` and `git log --oneline -5` to review the story before committing
- If SDD forecasts >400 lines, group commits into chained PR slices BEFORE implementation

## Project Conventions

| File | Path | Notes |
|------|------|-------|
| (none found) | — | No project-level convention files detected at init time |

Read the convention files listed above for project-specific patterns and rules. All referenced paths have been extracted — no need to read index files to discover more.
