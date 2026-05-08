// Package docker manages container lifecycle for Arnold agent sessions.
//
// Arnold uses ephemeral containers: docker run -it --rm with a custom
// entrypoint that handles auth, SSH, git config, plugin setup, and
// workspace copy-on-write. The container lives as long as the Claude session.
//
// Shell assumptions: commands delivered via tmux traverse two shell layers
// (tmux's implicit /bin/sh -c and wrapIgnoreSuspend's bash -c). Values in
// ExecPrefix / ExecPrefixWithEnv are therefore unquoted — quoting is applied
// once at the wrapIgnoreSuspend boundary.
package docker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"
)

// Container manages a single Docker container lifecycle.
type Container struct {
	// name is the container name (e.g. "arnold-a1b2c3d4").
	name string

	// image is the Docker image to use.
	image string
}

// NewContainer creates a container handle with the given name and image.
func NewContainer(name string, image string) *Container {
	if image == "" {
		image = defaultImage
	}
	return &Container{name: name, image: image}
}

// FromName creates a container handle for an existing container by name.
// The returned handle supports lifecycle operations (Exists, IsRunning, Start,
// Stop, Remove, ExecPrefix) but not Create — use NewContainer for that.
func FromName(name string) *Container {
	return &Container{name: name}
}

// GenerateName builds a container name from a session ID and human-readable title.
// Format: arnold-{title}-{id8}. The 8-char ID suffix guarantees uniqueness;
// the title is just for human readability in docker ps output.
func GenerateName(sessionID string, sessionTitle string) string {
	const idLen = 8 // First 8 chars of the session UUID — enough for uniqueness.
	id := sessionID
	if len(id) > idLen {
		id = id[:idLen]
	}
	sanitized := sanitizeContainerName(sessionTitle)
	if sanitized == "" {
		return containerNamePrefix + id
	}
	return containerNamePrefix + sanitized + "-" + id
}

// sanitizeContainerName strips characters not allowed in Docker container names
// ([a-zA-Z0-9_.-]) and truncates for readability.
func sanitizeContainerName(name string) string {
	const maxLen = 30
	var b strings.Builder
	for _, c := range name {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-':
			b.WriteRune(c)
		case c == ' ':
			b.WriteByte('-')
		}
	}
	result := b.String()
	// Trim leading/trailing hyphens and dots (Docker rejects these).
	result = strings.Trim(result, "-.")
	if len(result) > maxLen {
		result = result[:maxLen]
		result = strings.TrimRight(result, "-.")
	}
	return result
}

// Name returns the container name.
func (c *Container) Name() string {
	return c.name
}

// Exists returns true if the container exists (running or stopped).
// A non-zero exit code from docker inspect indicates the container does not exist.
// Other errors (e.g. Docker daemon unreachable) are propagated.
func (c *Container) Exists(ctx context.Context) (bool, error) {
	out, err := exec.CommandContext(ctx,
		"docker", "inspect",
		"--format", "{{.State.Status}}",
		c.name,
	).CombinedOutput()
	if err != nil {
		if isExitError(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspecting container %s: %s: %w", c.name, strings.TrimSpace(string(out)), err)
	}
	return true, nil
}

// IsRunning returns true if the container is currently running.
// Uses exit code as the primary signal rather than parsing error messages.
func (c *Container) IsRunning(ctx context.Context) (bool, error) {
	out, err := exec.CommandContext(ctx,
		"docker", "inspect",
		"--format", "{{.State.Running}}",
		c.name,
	).CombinedOutput()
	if err != nil {
		if isExitError(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspecting container %s: %w", c.name, err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// RunCommand builds the full "docker run" command args for an ephemeral Arnold container.
// The container runs with --privileged and --rm (auto-cleanup on exit).
// The entrypoint handles auth, SSH, git config, plugin setup, and workspace copy.
// Returns the args slice (without "docker" prefix) suitable for exec.Command or ShellJoinArgs.
func (c *Container) RunCommand(cfg *ContainerConfig, toolCommand ...string) []string {
	if cfg == nil {
		return nil
	}

	args := []string{
		"run", "-it", "--rm",
		"--name", c.name,
		"--label", "managed-by=arnold",
		"--privileged",
	}

	if cfg.workingDir != "" {
		args = append(args, "-e", fmt.Sprintf("CLAUDE_WORKDIR=%s", cfg.workingDir))
	}

	// Bind mounts.
	for _, v := range cfg.volumes {
		mount := fmt.Sprintf("%s:%s", v.hostPath, v.containerPath)
		if v.readOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	// Anonymous volumes (excluded directories get their own layer).
	for _, anonVol := range cfg.anonymousVolumes {
		args = append(args, "-v", anonVol)
	}

	// Environment variables. Keys sorted for deterministic output.
	for _, k := range slices.Sorted(maps.Keys(cfg.environment)) {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, cfg.environment[k]))
	}

	// Resource limits.
	if cfg.cpuLimit != "" {
		args = append(args, "--cpus", cfg.cpuLimit)
	}
	if cfg.memoryLimit != "" {
		args = append(args, "--memory", cfg.memoryLimit)
	}

	args = append(args, c.image)

	// Tool command (e.g. "claude", "--dangerously-skip-permissions").
	// If empty, the image's CMD is used.
	args = append(args, toolCommand...)

	return args
}

// Stop gracefully stops a running container.
func (c *Container) Stop(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "docker", "stop", c.name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stopping container %s: %s: %w", c.name, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Remove removes the container and its anonymous volumes.
// If force is true, a running container is killed first.
// If the container does not exist, this is a no-op.
func (c *Container) Remove(ctx context.Context, force bool) error {
	args := []string{"rm", "-v"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, c.name)

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		// Idempotent: container already gone is not an error.
		if isExitError(err) && strings.Contains(strings.ToLower(outStr), "no such container") {
			return nil
		}
		return fmt.Errorf("removing container %s: %s: %w", c.name, outStr, err)
	}
	return nil
}

// ExecPrefix returns the command prefix for running a command inside this container.
// Returns ["docker", "exec", "-it", name].
func (c *Container) ExecPrefix() []string {
	return []string{"docker", "exec", "-it", c.name}
}

// ExecPrefixNonInteractive returns the command prefix for non-interactive
// execution inside this container. Returns ["docker", "exec", name].
func (c *Container) ExecPrefixNonInteractive() []string {
	return []string{"docker", "exec", c.name}
}

// ExecPrefixWithEnv returns the command prefix with -e flags for runtime env vars.
// Each token is a discrete argument suitable for exec.Command (no shell quoting).
// Use ShellJoinArgs to convert to a shell-safe string when embedding in bash -c.
// Keys are sorted for deterministic output.
func (c *Container) ExecPrefixWithEnv(env map[string]string) []string {
	args := []string{"docker", "exec", "-it"}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, env[k]))
	}
	args = append(args, c.name)
	return args
}

// ShellJoinArgs joins command arguments into a shell-safe string.
// Each argument is single-quoted to prevent shell interpretation of special
// characters (spaces, quotes, $, backticks, semicolons, etc.).
// Arguments that are simple (alphanumeric, hyphens, underscores, dots, slashes,
// equals, colons, commas) are left unquoted for readability.
func ShellJoinArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuoteArg(arg)
	}
	return strings.Join(quoted, " ")
}

// shellQuoteArg returns a shell-safe representation of a single argument.
// Simple arguments are returned as-is; others are single-quoted with
// internal single quotes escaped via the '"'"' pattern.
func shellQuoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	// Safe chars that don't need quoting in POSIX shell.
	safe := true
	for _, c := range arg {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '/' || c == '=' || c == ':' || c == ',') {
			safe = false
			break
		}
	}
	if safe {
		return arg
	}
	// Single-quote the argument, escaping any internal single quotes.
	escaped := strings.ReplaceAll(arg, `'`, `'"'"'`)
	return "'" + escaped + "'"
}

// EnsureImage checks that image exists locally, pulling it if missing.
func EnsureImage(ctx context.Context, image string) error {
	if imageExistsLocally(ctx, image) {
		return nil
	}
	return pullImage(ctx, image)
}

// NewContainerConfig creates a ContainerConfig for an Arnold session.
// projectPath is the host directory to mount as /workspace (must be non-empty).
// Optional ContainerConfigOption functions customize mounts, limits, and environment.
func NewContainerConfig(projectPath string, opts ...ContainerConfigOption) *ContainerConfig {
	cfg := &ContainerConfig{
		workingDir:    containerWorkDir,
		containerHome: containerHome,
		environment:   make(map[string]string),
	}

	// Mount project directory. WithCopyWorkspace will convert this to /workspace-src:ro.
	if projectPath != "" {
		cfg.volumes = append(cfg.volumes, VolumeMount{
			hostPath:      projectPath,
			containerPath: containerWorkDir,
		})
	}

	// Apply caller-supplied options (WithCopyWorkspace, WithArnoldAuth, etc.).
	for _, opt := range opts {
		opt(cfg)
	}

	// TERM for proper TUI rendering inside the container.
	if cfg.environment["TERM"] == "" {
		cfg.environment["TERM"] = "xterm-256color"
	}

	return cfg
}

// ListManagedContainers returns names of all containers with the managed-by=arnold label.
func ListManagedContainers(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx,
		"docker", "ps", "-a",
		"--filter", "label=managed-by=arnold",
		"--format", "{{.Names}}",
	).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("listing managed containers: %s: %w", strings.TrimSpace(string(out)), err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// imageExistsLocally returns true if the image is available in the local docker cache.
func imageExistsLocally(ctx context.Context, image string) bool {
	err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Run()
	return err == nil
}

// pullImage pulls a docker image from the registry.
func pullImage(ctx context.Context, image string) error {
	out, err := exec.CommandContext(ctx, "docker", "pull", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pulling image %s: %s: %w", image, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// isExitError returns true if the error is an exec.ExitError (non-zero exit code).
func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}
