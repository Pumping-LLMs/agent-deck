# Arnold Integration - Design Spec

## Overview

Fork of agent-deck adapted to use Arnold's Docker container lifecycle (ephemeral `docker run -it --rm` with custom entrypoint) instead of Agent Deck's default sandbox model (persistent `docker create` + `sleep infinity` + `docker exec`). Claude-only (Gemini/Codex/OpenCode support removed). Keeps Agent Deck's TUI, conductors, cost tracking, groups, worktrees, and session management.

## Architecture Change: Container Lifecycle

### Before (Agent Deck sandbox model)
```
docker create --cap-drop=ALL --read-only --user uid:gid ... image sleep infinity
docker start <container>
docker exec -it <container> bash -c 'claude ...'  (repeated per command)
docker stop <container>
docker rm <container>
```

### After (Arnold model)
```
docker run -it --rm --name <name> --privileged \
  -e CLAUDE_CREDENTIALS=... -e GH_TOKEN=... \
  -v project:/workspace-src:ro \
  -e ARNOLD_COPY_WORKSPACE=1 \
  arnold-claude claude --dangerously-skip-permissions
```

Container lives as long as the Claude session. `--rm` cleans up on exit.
Shell access via `docker exec -it <name> bash` while container runs.

## Key Changes

### internal/docker/config.go
- `containerHome` → `/home/arnold`
- `defaultImage` → `arnold-claude:latest`
- `containerNamePrefix` → `arnold-`
- Remove `agentConfigMounts` for Gemini/OpenCode/Codex (keep Claude entry simplified)
- Remove security blocklists (Arnold runs --privileged)
- Add `WithArnoldAuth(credentials, apiKey, ghToken)` option for env var auth
- Add `WithCopyWorkspace()` option for copy-on-write mode
- Add `WithArnoldEntrypoint()` option

### internal/docker/docker.go
- `Create()` → replaced by `RunCommand()` returning `[]string` for the full docker run command
- Remove security hardening: `--cap-drop=ALL`, `--security-opt`, `--read-only`, `--user`, tmpfs
- Add `--privileged` flag
- Keep `ExecPrefix()` for shell access (T key)
- Keep `Exists()`, `IsRunning()`, `Stop()`, `Remove()` for lifecycle checks
- Remove `Start()` (no persistent containers)

### internal/docker/sandbox.go
- Simplify `RefreshAgentConfigs` - Arnold handles auth via entrypoint, not dir sync
- Remove multi-agent config handling
- Keep credential cleanup

### internal/session/instance.go
- `ensureSandboxContainer` → builds docker run command string instead of create+exec
- `buildSandboxConfig` → passes Arnold-specific env vars and mounts
- `buildExecCommand` → returns the docker run command (not docker exec)
- `wrapForSandbox` → returns docker run command for tmux

## What Stays Unchanged
- TUI (Bubble Tea)
- Conductors
- Cost tracking
- Session management / storage
- Worktree management
- Groups
- tmux integration (sessions still managed via tmux)
