package docker

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// containerHome is the home directory inside the Arnold container.
	// The arnold user is created in the Dockerfile; the entrypoint drops
	// privileges from root to arnold after setup.
	containerHome = "/home/arnold"

	// containerNamePrefix is the expected prefix for managed containers.
	containerNamePrefix = "arnold-"

	// containerWorkDir is the workspace inside the container.
	containerWorkDir = "/workspace"

	// defaultImage is the Arnold Docker image (locally built).
	defaultImage = "arnold-claude:latest"
)

// agentConfigMounts defines Claude config directories synced into containers.
// Arnold's entrypoint handles auth (OAuth/API key) via env vars, so only
// plugins and skills need host-to-container sync.
var agentConfigMounts = []AgentConfigMount{
	{
		hostRel:         ".claude",
		containerSuffix: ".claude",
		skipEntries:     []string{"sandbox", "projects", ".home-seeds"},
		copyDirs:        []string{"plugins", "skills"},
		homeSeedFiles:   map[string]string{".claude.json": `{"hasCompletedOnboarding":true}`},
		preserveFiles:   []string{".credentials.json", "statsig_user_id"},
		keychainCredential: &keychainEntry{
			service:  "Claude Code-credentials",
			filename: ".credentials.json",
		},
	},
}

// Arnold runs with --privileged and a trusted entrypoint, so no mount
// blocklists are needed. The container is ephemeral (--rm) and isolated
// by design through copy-on-write workspace mounting.

// keychainEntry describes a macOS Keychain credential to extract.
type keychainEntry struct {
	// service is the Keychain service name (e.g. "Claude Code-credentials").
	service string
	// filename is the target filename inside the sandbox (e.g. ".credentials.json").
	filename string
}

// VolumeMount represents a single bind mount.
type VolumeMount struct {
	hostPath      string
	containerPath string
	readOnly      bool
}

// ContainerConfig holds settings for container creation.
// All fields are unexported to enforce construction via NewContainerConfig and the
// options pattern (WithGitConfig, WithSSH, etc.), preventing partially initialized configs.
type ContainerConfig struct {
	// workingDir inside the container.
	workingDir string

	// containerHome is the home directory inside the container (default: /root).
	// Override with WithContainerHome for non-root images.
	containerHome string

	// volumes are bind mounts (host path → container path).
	volumes []VolumeMount

	// anonymousVolumes are container-only paths (e.g. /workspace/node_modules).
	anonymousVolumes []string

	// environment variables to set in the container.
	environment map[string]string

	// cpuLimit is the CPU quota (e.g. "2.0").
	cpuLimit string

	// memoryLimit is the memory cap (e.g. "4g").
	memoryLimit string
}

// ContainerConfigOption customizes a ContainerConfig during NewContainerConfig.
type ContainerConfigOption func(*ContainerConfig)

// AgentConfigMount declares how a tool's config directory is synced into sandbox containers.
type AgentConfigMount struct {
	// hostRel is the path relative to home (e.g. ".claude").
	hostRel string
	// containerSuffix is the path relative to container home (e.g. ".claude").
	containerSuffix string
	// skipEntries are top-level entry names to skip when copying (not recursive).
	// Only exact matches on direct children of the host dir are excluded.
	skipEntries []string
	// copyDirs are directories to recursively copy into the sandbox.
	copyDirs []string
	// seedFiles are files written only if absent (write-once) to preserve container state (name → content).
	seedFiles map[string]string
	// homeSeedFiles are files to seed at the container home level, outside the config dir (name → content).
	homeSeedFiles map[string]string
	// preserveFiles are filenames that should not be overwritten if they already exist in the sandbox.
	// Unlike seedFiles (which write initial content), preserveFiles protect container-generated state
	// from being overwritten by host-to-sandbox copies (e.g. credentials, history).
	preserveFiles []string
	// keychainCredential is a macOS Keychain credential to extract (nil on Linux).
	keychainCredential *keychainEntry
}

// WithAgentConfigs adds bind mounts from sandbox sync results.
func WithAgentConfigs(bindMounts []VolumeMount, homeMounts []VolumeMount) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		cfg.volumes = append(cfg.volumes, bindMounts...)
		cfg.volumes = append(cfg.volumes, homeMounts...)
	}
}

// DefaultImage returns the default sandbox image name.
func DefaultImage() string {
	return defaultImage
}

// IsManagedContainer returns true if the name matches the agent-deck naming convention.
func IsManagedContainer(name string) bool {
	return len(name) > len(containerNamePrefix) && strings.HasPrefix(name, containerNamePrefix)
}

// AgentConfigMounts returns a shallow clone of all tool config mount definitions.
// Callers receive their own slice and cannot mutate the package-level configuration.
func AgentConfigMounts() []AgentConfigMount {
	return slices.Clone(agentConfigMounts)
}

// WithContainerHome overrides the default container home directory.
func WithContainerHome(home string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if home != "" {
			cfg.containerHome = home
		}
	}
}

// WithArnoldAuth sets Arnold-style authentication via environment variables.
// The entrypoint reads these and writes credentials/config files.
func WithArnoldAuth(credentials string, apiKey string, ghToken string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if credentials != "" {
			cfg.environment["CLAUDE_CREDENTIALS"] = credentials
		}
		if apiKey != "" {
			cfg.environment["ANTHROPIC_API_KEY"] = apiKey
		}
		if ghToken != "" {
			cfg.environment["GH_TOKEN"] = ghToken
		}
	}
}

// WithCopyWorkspace enables Arnold's copy-on-write workspace mode.
// The project is mounted read-only at /workspace-src and the entrypoint
// copies it to /workspace/<name> so edits don't affect the host.
func WithCopyWorkspace(repoName string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		cfg.environment["ARNOLD_COPY_WORKSPACE"] = "1"
		cfg.environment["WORKSPACE_NAME"] = repoName
		// Replace the default rw mount with a ro mount at /workspace-src
		newVols := make([]VolumeMount, 0, len(cfg.volumes))
		for _, v := range cfg.volumes {
			if v.containerPath == containerWorkDir {
				newVols = append(newVols, VolumeMount{
					hostPath:      v.hostPath,
					containerPath: "/workspace-src",
					readOnly:      true,
				})
			} else {
				newVols = append(newVols, v)
			}
		}
		cfg.volumes = newVols
		cfg.workingDir = containerWorkDir + "/" + repoName
	}
}

// WithGitIdentity sets git user name and email via env vars for the entrypoint.
func WithGitIdentity(name string, email string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if name != "" {
			cfg.environment["GIT_USER_NAME"] = name
		}
		if email != "" {
			cfg.environment["GIT_USER_EMAIL"] = email
		}
	}
}

// WithClaudeConfig mounts the host ~/.claude directory read-only so the
// entrypoint can copy plugins, skills, and settings into the container.
func WithClaudeConfig(claudeDir string, claudeJSON string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if claudeDir != "" {
			cfg.volumes = append(cfg.volumes, VolumeMount{
				hostPath:      claudeDir,
				containerPath: "/tmp/.claude-host",
				readOnly:      true,
			})
		}
		if claudeJSON != "" {
			cfg.volumes = append(cfg.volumes, VolumeMount{
				hostPath:      claudeJSON,
				containerPath: "/tmp/.claude.json",
				readOnly:      true,
			})
		}
	}
}

// WithGitConfig mounts the host gitconfig file read-only inside the container.
// Arnold's entrypoint also supports GIT_USER_NAME/GIT_USER_EMAIL env vars
// via WithGitIdentity, which takes precedence.
func WithGitConfig(path string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if path == "" {
			return
		}
		cfg.volumes = append(cfg.volumes, VolumeMount{
			hostPath:      path,
			containerPath: cfg.containerHome + "/.gitconfig",
			readOnly:      true,
		})
	}
}

// WithSSH mounts the host ~/.ssh directory read-only at Arnold's SSH staging path.
// The entrypoint copies keys to /home/arnold/.ssh with correct permissions.
func WithSSH(path string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if path == "" {
			return
		}
		cfg.volumes = append(cfg.volumes, VolumeMount{
			hostPath:      path,
			containerPath: "/root/.ssh-keys",
			readOnly:      true,
		})
	}
}

// WithVolumeIgnores converts directory names into anonymous volumes at /workspace/<dir>.
// This prevents large host directories (e.g. node_modules) from syncing into the container.
// Entries containing path separators or ".." are rejected to prevent path traversal.
func WithVolumeIgnores(dirs []string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		for _, dir := range dirs {
			if dir == "" || strings.Contains(dir, "..") || strings.ContainsAny(dir, "/\\") {
				continue
			}
			cfg.anonymousVolumes = append(cfg.anonymousVolumes, filepath.Join(containerWorkDir, dir))
		}
	}
}

// WithWorktree mounts the full repository root and adjusts workingDir to the worktree subdirectory.
// repoRoot is the absolute host path to the git repository root.
// relativePath is the worktree path relative to repoRoot.
func WithWorktree(repoRoot string, relativePath string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if repoRoot == "" {
			return
		}
		// Rebuild volumes slice, replacing the project mount with the repo root.
		newVols := make([]VolumeMount, 0, len(cfg.volumes))
		for _, v := range cfg.volumes {
			if v.containerPath == containerWorkDir {
				newVols = append(newVols, VolumeMount{
					hostPath:      repoRoot,
					containerPath: containerWorkDir,
				})
			} else {
				newVols = append(newVols, v)
			}
		}
		cfg.volumes = newVols
		// Adjust working directory to the worktree subdirectory.
		if relativePath != "" {
			cfg.workingDir = filepath.Join(containerWorkDir, relativePath)
		}
	}
}

// WithExtraVolumes adds user-configured bind mounts (host → container path).
// Both paths must be absolute. Arnold runs --privileged so no blocklists apply.
func WithExtraVolumes(volumes map[string]string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		for host, container := range volumes {
			if host == "" || container == "" {
				continue
			}
			cleanHost := filepath.Clean(host)
			if !filepath.IsAbs(cleanHost) {
				continue
			}
			cleanContainer := filepath.Clean(container)
			if !filepath.IsAbs(cleanContainer) {
				continue
			}
			cfg.volumes = append(cfg.volumes, VolumeMount{
				hostPath:      cleanHost,
				containerPath: cleanContainer,
			})
		}
	}
}

// WithMultiRepoPaths replaces the default single-project mount with one mount per
// project path, each under /workspace/<dirname>. The container working directory
// is set to /workspace so the agent can navigate between repos.
func WithMultiRepoPaths(paths []string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		// Remove the default project mount at containerWorkDir
		newVols := make([]VolumeMount, 0, len(cfg.volumes)+len(paths))
		for _, v := range cfg.volumes {
			if v.containerPath == containerWorkDir {
				continue
			}
			newVols = append(newVols, v)
		}
		// Mount each path under /workspace/<dirname>, deduplicating names
		seen := make(map[string]int)
		for _, p := range paths {
			dirname := filepath.Base(p)
			if n := seen[dirname]; n > 0 {
				dirname = fmt.Sprintf("%s-%d", dirname, n)
			}
			seen[filepath.Base(p)]++
			newVols = append(newVols, VolumeMount{
				hostPath:      p,
				containerPath: filepath.Join(containerWorkDir, dirname),
			})
		}
		cfg.volumes = newVols
		cfg.workingDir = containerWorkDir
	}
}

// WithCPULimit sets the CPU quota for the container (e.g. "2.0").
func WithCPULimit(limit string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if limit != "" {
			cfg.cpuLimit = limit
		}
	}
}

// WithMemoryLimit sets the memory cap for the container (e.g. "4g").
func WithMemoryLimit(limit string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if limit != "" {
			cfg.memoryLimit = limit
		}
	}
}

// WithEnvironment merges key=value environment variables into the container config.
// Used for both host-resolved variables (via collectDockerEnvVars) and static values.
func WithEnvironment(env map[string]string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if len(env) == 0 {
			return
		}
		if cfg.environment == nil {
			cfg.environment = make(map[string]string, len(env))
		}
		maps.Copy(cfg.environment, env)
	}
}

// ContainerPath returns the full container path for this mount.
// The home parameter is the container's home directory (e.g. containerHome).
func (m AgentConfigMount) ContainerPath(home string) string {
	return home + "/" + m.containerSuffix
}
