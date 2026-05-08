package docker

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sessionID    string
		sessionTitle string
		want         string
	}{
		{
			name:         "id only when title empty",
			sessionID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			sessionTitle: "",
			want:         "arnold-a1b2c3d4",
		},
		{
			name:         "title included",
			sessionID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			sessionTitle: "my-refactor",
			want:         "arnold-my-refactor-a1b2c3d4",
		},
		{
			name:         "title with spaces converted to hyphens",
			sessionID:    "a1b2c3d4-e5f6",
			sessionTitle: "auth module",
			want:         "arnold-auth-module-a1b2c3d4",
		},
		{
			name:         "title with special chars stripped",
			sessionID:    "a1b2c3d4-e5f6",
			sessionTitle: "fix: bug #42!",
			want:         "arnold-fix-bug-42-a1b2c3d4",
		},
		{
			name:         "short id preserved",
			sessionID:    "abc",
			sessionTitle: "test",
			want:         "arnold-test-abc",
		},
		{
			name:         "long title truncated",
			sessionID:    "12345678",
			sessionTitle: "this-is-a-very-long-session-title-that-exceeds-the-limit",
			want:         "arnold-this-is-a-very-long-session-ti-12345678",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := GenerateName(tc.sessionID, tc.sessionTitle)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestNewContainer_DefaultImage(t *testing.T) {
	t.Parallel()

	c := NewContainer("test-container", "")
	require.Equal(t, "test-container", c.Name())
	require.Equal(t, defaultImage, c.image)
}

func TestNewContainer_CustomImage(t *testing.T) {
	t.Parallel()

	c := NewContainer("test-container", "my-image:latest")
	require.Equal(t, "my-image:latest", c.image)
}

func TestFromName(t *testing.T) {
	t.Parallel()

	c := FromName("existing-container")
	require.Equal(t, "existing-container", c.Name())
}

func TestNewContainerConfig_ProjectMount(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/home/user/project")

	// Should have project mount.
	require.Len(t, cfg.volumes, 1)
	require.Equal(t, "/home/user/project", cfg.volumes[0].hostPath)
	require.Equal(t, containerWorkDir, cfg.volumes[0].containerPath)
	require.False(t, cfg.volumes[0].readOnly)
}

func TestNewContainerConfig_ResourceLimits(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project", WithCPULimit("2.0"), WithMemoryLimit("4g"))
	require.Equal(t, "2.0", cfg.cpuLimit)
	require.Equal(t, "4g", cfg.memoryLimit)
}

func TestNewContainerConfig_Environment(t *testing.T) {
	t.Parallel()

	extra := map[string]string{"MY_VAR": "my_value"}
	cfg := NewContainerConfig("/project", WithEnvironment(extra))

	// Extra env passed through.
	require.Equal(t, "my_value", cfg.environment["MY_VAR"])
	// TERM is set by default.
	require.Equal(t, "xterm-256color", cfg.environment["TERM"])
}

func TestWithEnvironment_NilMap(t *testing.T) {
	t.Parallel()

	cfg := &ContainerConfig{}
	opt := WithEnvironment(map[string]string{"KEY": "val"})
	opt(cfg)
	require.Equal(t, "val", cfg.environment["KEY"])
}

func TestExecPrefix(t *testing.T) {
	t.Parallel()

	c := NewContainer("my-container", "")
	prefix := c.ExecPrefix()
	require.Equal(t, []string{"docker", "exec", "-it", "my-container"}, prefix)
}

func TestExecPrefixNonInteractive(t *testing.T) {
	t.Parallel()

	c := NewContainer("my-container", "")
	prefix := c.ExecPrefixNonInteractive()
	require.Equal(t, []string{"docker", "exec", "my-container"}, prefix)
}

func TestDefaultImage(t *testing.T) {
	t.Parallel()

	require.Equal(t, defaultImage, DefaultImage())
}

func TestAgentConfigMounts_ClaudeOnly(t *testing.T) {
	t.Parallel()

	mounts := AgentConfigMounts()
	// Arnold only has Claude config mount.
	require.Len(t, mounts, 1)
	require.Equal(t, ".claude", mounts[0].hostRel)
	require.Contains(t, mounts[0].skipEntries, "sandbox")
}

func TestIsManagedContainer(t *testing.T) {
	t.Parallel()

	require.True(t, IsManagedContainer("arnold-a1b2c3d4"))
	require.False(t, IsManagedContainer("my-production-container"))
	require.False(t, IsManagedContainer("arnold-"))
}

func TestExecPrefixWithEnv(t *testing.T) {
	t.Parallel()

	c := NewContainer("sandbox", "")
	env := map[string]string{
		"B_VAR": "b",
		"A_VAR": "a",
	}
	prefix := c.ExecPrefixWithEnv(env)
	require.Equal(t, []string{
		"docker", "exec", "-it",
		"-e", "A_VAR=a",
		"-e", "B_VAR=b",
		"sandbox",
	}, prefix)
}

func TestExecPrefixWithEnv_Empty(t *testing.T) {
	t.Parallel()

	c := NewContainer("sandbox", "")
	prefix := c.ExecPrefixWithEnv(nil)
	require.Equal(t, []string{"docker", "exec", "-it", "sandbox"}, prefix)
}

func TestExecPrefixWithEnv_SpecialChars(t *testing.T) {
	t.Parallel()

	c := NewContainer("sandbox", "")
	env := map[string]string{
		"VAR": `value with "quotes" and $dollar`,
	}
	prefix := c.ExecPrefixWithEnv(env)
	require.Equal(t, []string{
		"docker", "exec", "-it",
		"-e", `VAR=value with "quotes" and $dollar`,
		"sandbox",
	}, prefix)
}

func TestNewContainerConfig_GitConfig(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithGitConfig("/home/user/.gitconfig"),
	)

	require.Len(t, cfg.volumes, 2)
	require.Equal(t, "/home/user/.gitconfig", cfg.volumes[1].hostPath)
	require.Equal(t, containerHome+"/.gitconfig", cfg.volumes[1].containerPath)
	require.True(t, cfg.volumes[1].readOnly)
}

func TestNewContainerConfig_GitConfig_Empty(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithGitConfig(""),
	)

	require.Len(t, cfg.volumes, 1)
}

func TestNewContainerConfig_SSH_ArnoldStagingPath(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithSSH("/home/user/.ssh"),
	)

	require.Len(t, cfg.volumes, 2)
	require.Equal(t, "/home/user/.ssh", cfg.volumes[1].hostPath)
	// Arnold mounts SSH at /root/.ssh-keys for the entrypoint to copy.
	require.Equal(t, "/root/.ssh-keys", cfg.volumes[1].containerPath)
	require.True(t, cfg.volumes[1].readOnly)
}

func TestNewContainerConfig_VolumeIgnores(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithVolumeIgnores([]string{"node_modules", ".venv"}),
	)

	require.Equal(t, []string{"/workspace/node_modules", "/workspace/.venv"}, cfg.anonymousVolumes)
}

func TestNewContainerConfig_VolumeIgnores_RejectsTraversal(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithVolumeIgnores([]string{"../../etc", "safe", "sub/dir", ".."}),
	)

	require.Equal(t, []string{"/workspace/safe"}, cfg.anonymousVolumes)
}

func TestNewContainerConfig_ExtraVolumes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			dir: "/container/data",
		}),
	)

	// Project mount + extra volume. Arnold has no blocklists.
	require.Len(t, cfg.volumes, 2)
	require.Equal(t, filepath.Clean(dir), cfg.volumes[1].hostPath)
	require.Equal(t, "/container/data", cfg.volumes[1].containerPath)
}

func TestNewContainerConfig_ExtraVolumes_SkipsEmpty(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			"":           "/container/path",
			"/host/path": "",
		}),
	)

	require.Len(t, cfg.volumes, 1)
}

func TestNewContainerConfig_ExtraVolumes_RejectsRelativePaths(t *testing.T) {
	t.Parallel()

	safeDir := t.TempDir()

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			"relative/path":    "/container/data",
			"../../etc/passwd": "/data/passwd",
			safeDir:            "relative/container",
		}),
	)

	require.Len(t, cfg.volumes, 1)

	cfg2 := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			safeDir: "/safe/container",
		}),
	)

	require.Len(t, cfg2.volumes, 2)
	require.Equal(t, "/safe/container", cfg2.volumes[1].containerPath)
}

func TestRunCommand_NilConfig(t *testing.T) {
	t.Parallel()

	c := NewContainer("test", "image:latest")
	args := c.RunCommand(nil)
	require.Nil(t, args)
}

func TestRunCommand_Basic(t *testing.T) {
	t.Parallel()

	c := NewContainer("arnold-test-123", "arnold-claude:latest")
	cfg := NewContainerConfig("/project")
	args := c.RunCommand(cfg, "claude", "--dangerously-skip-permissions")

	require.Contains(t, args, "run")
	require.Contains(t, args, "-it")
	require.Contains(t, args, "--rm")
	require.Contains(t, args, "--privileged")
	require.Contains(t, args, "arnold-test-123")
	require.Contains(t, args, "arnold-claude:latest")
	require.Contains(t, args, "claude")
	require.Contains(t, args, "--dangerously-skip-permissions")
}

func TestRunCommand_WithEnv(t *testing.T) {
	t.Parallel()

	c := NewContainer("arnold-test", "arnold-claude:latest")
	cfg := NewContainerConfig("/project",
		WithEnvironment(map[string]string{"MY_VAR": "hello"}),
	)
	args := c.RunCommand(cfg)

	// Check env vars are in the args.
	found := false
	for i, arg := range args {
		if arg == "-e" && i+1 < len(args) && args[i+1] == "MY_VAR=hello" {
			found = true
			break
		}
	}
	require.True(t, found, "expected MY_VAR=hello in args")
}

func TestNewContainerConfig_Worktree(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/worktree/feature-x",
		WithWorktree("/repo-root", "worktrees/feature-x"),
	)

	require.Equal(t, "/repo-root", cfg.volumes[0].hostPath)
	require.Equal(t, containerWorkDir, cfg.volumes[0].containerPath)
	require.Equal(t, "/workspace/worktrees/feature-x", cfg.workingDir)
}

func TestNewContainerConfig_Worktree_NoRelativePath(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithWorktree("/repo-root", ""),
	)

	require.Equal(t, "/repo-root", cfg.volumes[0].hostPath)
	require.Equal(t, containerWorkDir, cfg.workingDir)
}

func TestNewContainerConfig_ContainerHome(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithContainerHome("/home/app"),
		WithGitConfig("/home/user/.gitconfig"),
	)

	require.Len(t, cfg.volumes, 2)
	require.Equal(t, "/home/app/.gitconfig", cfg.volumes[1].containerPath)
}

func TestNewContainerConfig_ContainerHome_Default(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project")
	require.Equal(t, containerHome, cfg.containerHome)
	require.Equal(t, "/home/arnold", cfg.containerHome)
}

func TestNewContainerConfig_EmptyProjectPath(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("")

	require.Empty(t, cfg.volumes)
}

func TestNewContainerConfig_DefaultTERM(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project")
	require.Equal(t, "xterm-256color", cfg.environment["TERM"])
}

func TestWithArnoldAuth(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithArnoldAuth("oauth-creds-json", "sk-ant-123", "ghp_token"),
	)

	require.Equal(t, "oauth-creds-json", cfg.environment["CLAUDE_CREDENTIALS"])
	require.Equal(t, "sk-ant-123", cfg.environment["ANTHROPIC_API_KEY"])
	require.Equal(t, "ghp_token", cfg.environment["GH_TOKEN"])
}

func TestWithArnoldAuth_Empty(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithArnoldAuth("", "", ""),
	)

	_, hasCreds := cfg.environment["CLAUDE_CREDENTIALS"]
	_, hasKey := cfg.environment["ANTHROPIC_API_KEY"]
	_, hasGH := cfg.environment["GH_TOKEN"]
	require.False(t, hasCreds)
	require.False(t, hasKey)
	require.False(t, hasGH)
}

func TestWithCopyWorkspace(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/home/user/myrepo",
		WithCopyWorkspace("myrepo"),
	)

	// Project mount should be converted to /workspace-src:ro.
	require.Len(t, cfg.volumes, 1)
	require.Equal(t, "/home/user/myrepo", cfg.volumes[0].hostPath)
	require.Equal(t, "/workspace-src", cfg.volumes[0].containerPath)
	require.True(t, cfg.volumes[0].readOnly)

	// Env vars set for entrypoint.
	require.Equal(t, "1", cfg.environment["ARNOLD_COPY_WORKSPACE"])
	require.Equal(t, "myrepo", cfg.environment["WORKSPACE_NAME"])

	// Working dir adjusted.
	require.Equal(t, "/workspace/myrepo", cfg.workingDir)
}

func TestWithGitIdentity(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithGitIdentity("Arnold", "arnold@example.com"),
	)

	require.Equal(t, "Arnold", cfg.environment["GIT_USER_NAME"])
	require.Equal(t, "arnold@example.com", cfg.environment["GIT_USER_EMAIL"])
}

func TestWithClaudeConfig(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithClaudeConfig("/home/user/.claude", "/home/user/.claude.json"),
	)

	// Two additional mounts for Claude config.
	require.Len(t, cfg.volumes, 3)
	require.Equal(t, "/tmp/.claude-host", cfg.volumes[1].containerPath)
	require.True(t, cfg.volumes[1].readOnly)
	require.Equal(t, "/tmp/.claude.json", cfg.volumes[2].containerPath)
	require.True(t, cfg.volumes[2].readOnly)
}

func TestShellJoinArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "simple alphanumeric args",
			args: []string{"docker", "exec", "-it", "my-container", "bash"},
			want: "docker exec -it my-container bash",
		},
		{
			name: "env value with spaces and semicolons",
			args: []string{
				"docker", "exec", "-it",
				"-e", "TERM=xterm-256color",
				"-e", "API_KEY=sk-ant abc;rm -rf /",
				"my-container", "claude",
			},
			want: `docker exec -it -e TERM=xterm-256color -e 'API_KEY=sk-ant abc;rm -rf /' my-container claude`,
		},
		{
			name: "double quotes and dollar signs",
			args: []string{"-e", `VAR=value with "quotes" and $dollar`},
			want: `-e 'VAR=value with "quotes" and $dollar'`,
		},
		{
			name: "single quotes in value",
			args: []string{"-e", "VAR=it's a value"},
			want: `-e 'VAR=it'"'"'s a value'`,
		},
		{
			name: "empty argument",
			args: []string{"cmd", ""},
			want: "cmd ''",
		},
		{
			name: "backticks and subshell",
			args: []string{"-e", "VAR=$(whoami)"},
			want: `-e 'VAR=$(whoami)'`,
		},
		{
			name: "newlines and tabs",
			args: []string{"-e", "VAR=line1\nline2\ttab"},
			want: `-e 'VAR=line1` + "\n" + `line2` + "\t" + `tab'`,
		},
		{
			name: "path with slashes and dots",
			args: []string{"/usr/bin/docker", "--config=/root/.docker"},
			want: "/usr/bin/docker --config=/root/.docker",
		},
		{
			name: "pipe and redirect chars",
			args: []string{"-e", "CMD=echo hello | cat > /tmp/out"},
			want: `-e 'CMD=echo hello | cat > /tmp/out'`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ShellJoinArgs(tc.args)
			require.Equal(t, tc.want, got)
		})
	}
}
