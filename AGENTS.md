<!-- TRELLIS:START -->
# Trellis Instructions

These instructions are for AI assistants working in this project.

This project is managed by Trellis. The working knowledge you need lives under `.trellis/`:

- `.trellis/workflow.md` — development phases, when to create tasks, skill routing
- `.trellis/spec/` — package- and layer-scoped coding guidelines (read before writing code in a given layer)
- `.trellis/workspace/` — per-developer journals and session traces
- `.trellis/tasks/` — active and archived tasks (PRDs, research, jsonl context)

If a Trellis command is available on your platform (e.g. `/trellis:finish-work`, `/trellis:continue`), prefer it over manual steps. Not every platform exposes every command.

If you're using Codex or another agent-capable tool, additional project-scoped helpers may live in:
- `.agents/skills/` — reusable Trellis skills
- `.codex/agents/` — optional custom subagents

Managed by Trellis. Edits outside this block are preserved; edits inside may be overwritten by a future `trellis update`.

<!-- TRELLIS:END -->

## Build / Test / Run

- Build: `go build ./cmd/gateway` (or `go build -o bin/gateway.exe ./cmd/gateway`)
- Test: `go test ./...`
- Vet: `go vet ./...`
- Run: `gateway.exe` with env `ADMIN_TOKEN` and `MASTER_KEY` required; see `internal/config` for all vars (`LISTEN_ADDR` default `:8080`, `DB_PATH` default `data/gateway.db`).
- Shell: Git for Windows is installed at non-default `D:\develop\Git`; the junction `%LOCALAPPDATA%\Programs\Git` → `D:\develop\Git` lets Devin find Git Bash. Without it, `bash` resolves to the WSL stub (no distro installed) and the Trellis hooks in `.claude/settings.json` fail with exit code 1.
- Only non-stdlib dependency: `modernc.org/sqlite` (pure Go, no cgo; `CGO_ENABLED=0` builds work).
