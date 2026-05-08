package docker

import (
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SandboxDir returns the shared sandbox directory path for a tool's config.
func SandboxDir(homeDir string, hostRel string) string {
	return filepath.Join(homeDir, hostRel, "sandbox")
}

// CleanupKeychainCredentials removes plaintext credential files that were
// extracted from the macOS Keychain during sandbox sync. Called on session
// teardown to avoid persisting secrets on the host filesystem.
func CleanupKeychainCredentials(homeDir string) {
	for _, mount := range agentConfigMounts {
		if mount.keychainCredential == nil {
			continue
		}
		credPath := filepath.Join(SandboxDir(homeDir, mount.hostRel), mount.keychainCredential.filename)
		if err := os.Remove(credPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("Removing keychain credential file", "path", credPath, "error", err)
		}
	}
}

// SyncAgentConfig syncs host tool config into a shared sandbox directory.
// For Arnold, this primarily syncs Claude plugins and skills.
func SyncAgentConfig(homeDir string, mount AgentConfigMount) (string, error) {
	hostDir := filepath.Join(homeDir, mount.hostRel)
	sandboxDir := SandboxDir(homeDir, mount.hostRel)

	if err := os.MkdirAll(sandboxDir, 0o700); err != nil {
		return "", fmt.Errorf("creating sandbox dir %s: %w", sandboxDir, err)
	}

	// Write seed files only if they don't already exist (write-once).
	for _, name := range slices.Sorted(maps.Keys(mount.seedFiles)) {
		content := mount.seedFiles[name]
		dest := filepath.Join(sandboxDir, name)
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
			slog.Warn("Writing seed file", "file", name, "error", err)
		}
	}

	// Copy top-level files from host dir (skip directories and skipEntries).
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		if os.IsNotExist(err) {
			return sandboxDir, nil
		}
		return "", fmt.Errorf("reading host dir %s: %w", hostDir, err)
	}

	skipSet := make(map[string]bool, len(mount.skipEntries))
	for _, s := range mount.skipEntries {
		skipSet[s] = true
	}

	copyDirSet := make(map[string]bool, len(mount.copyDirs))
	for _, d := range mount.copyDirs {
		copyDirSet[d] = true
	}

	preserveSet := make(map[string]bool, len(mount.preserveFiles))
	for _, f := range mount.preserveFiles {
		preserveSet[f] = true
	}

	for _, entry := range entries {
		name := entry.Name()
		if skipSet[name] {
			continue
		}

		src := filepath.Join(hostDir, name)

		resolved, err := resolveAndValidateSymlink(src, hostDir)
		if err != nil {
			slog.Warn("Skipping entry", "path", src, "error", err)
			continue
		}

		info, err := os.Stat(resolved)
		if err != nil {
			slog.Warn("Skipping entry (cannot stat)", "path", src, "error", err)
			continue
		}

		if info.IsDir() {
			if copyDirSet[name] {
				dest := filepath.Join(sandboxDir, name)
				if err := copyDirRecursive(resolved, dest); err != nil {
					slog.Warn("Copying directory", "src", resolved, "error", err)
				}
			}
			continue
		}

		dest := filepath.Join(sandboxDir, name)
		if preserveSet[name] {
			if _, statErr := os.Stat(dest); statErr == nil {
				continue
			}
		}

		if err := copyFile(resolved, dest); err != nil {
			slog.Warn("Copying file", "src", resolved, "error", err)
		}
	}

	// Extract macOS Keychain credential if configured.
	if mount.keychainCredential != nil {
		dest := filepath.Join(sandboxDir, mount.keychainCredential.filename)
		if err := extractKeychainCredential(mount.keychainCredential.service, dest); err != nil {
			slog.Warn("Extracting keychain credential", "service", mount.keychainCredential.service, "error", err)
		}
	}

	return sandboxDir, nil
}

// RefreshAgentConfigs syncs Claude config into shared sandbox directories.
// For Arnold, this syncs plugins, skills, and settings from ~/.claude.
// Auth is handled separately via env vars (WithArnoldAuth), not directory sync.
// cHome is the container's home directory (default: "/home/arnold").
func RefreshAgentConfigs(homeDir string, cHome string) (bindMounts []VolumeMount, homeMounts []VolumeMount) {
	if cHome == "" {
		cHome = containerHome
	}
	for _, mount := range agentConfigMounts {
		hostDir := filepath.Join(homeDir, mount.hostRel)

		if _, err := os.Stat(hostDir); err != nil {
			slog.Debug("Host config dir missing, skipping", "dir", hostDir)
			continue
		}

		sandboxDir, err := SyncAgentConfig(homeDir, mount)
		if err != nil {
			slog.Warn("Sandbox sync failed, skipping",
				"tool", mount.hostRel, "error", err)
			continue
		}

		bindMounts = append(bindMounts, VolumeMount{
			hostPath:      sandboxDir,
			containerPath: mount.ContainerPath(cHome),
		})

		if len(mount.homeSeedFiles) > 0 {
			seedDir := filepath.Join(sandboxDir, ".home-seeds")
			if mkErr := os.MkdirAll(seedDir, 0o700); mkErr != nil {
				slog.Warn("Creating home seed dir", "dir", seedDir, "error", mkErr)
			} else {
				for _, name := range slices.Sorted(maps.Keys(mount.homeSeedFiles)) {
					content := mount.homeSeedFiles[name]
					seedPath := filepath.Join(seedDir, name)
					if writeErr := os.WriteFile(seedPath, []byte(content), 0o600); writeErr != nil {
						slog.Warn("Writing home seed file", "file", name, "error", writeErr)
						continue
					}
					homeMounts = append(homeMounts, VolumeMount{
						hostPath:      seedPath,
						containerPath: cHome + "/" + name,
					})
				}
			}
		}
	}

	return bindMounts, homeMounts
}

// resolveAndValidateSymlink resolves a symlink and verifies the target is within
// boundaryDir.
func resolveAndValidateSymlink(entryPath string, boundaryDir string) (string, error) {
	resolved, err := filepath.EvalSymlinks(entryPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", entryPath, err)
	}
	if !pathWithin(resolved, boundaryDir) {
		return "", fmt.Errorf("symlink %s resolves outside boundary %s to %s", entryPath, boundaryDir, resolved)
	}
	return resolved, nil
}

// copyDirRecursive copies a directory tree from src to dst, following symlinks.
func copyDirRecursive(src string, dst string) error {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("resolving source %s: %w", src, err)
	}

	if _, err := os.Stat(absSrc); err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("creating dir %s: %w", dst, err)
	}

	entries, err := os.ReadDir(absSrc)
	if err != nil {
		return fmt.Errorf("reading dir %s: %w", src, err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(absSrc, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		resolved, err := resolveAndValidateSymlink(srcPath, absSrc)
		if err != nil {
			slog.Warn("Skipping entry in recursive copy", "path", srcPath, "error", err)
			continue
		}

		info, err := os.Stat(resolved)
		if err != nil {
			slog.Warn("Skipping entry in recursive copy", "path", srcPath, "error", err)
			continue
		}

		if info.IsDir() {
			if err := copyDirRecursive(resolved, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(resolved, dstPath); err != nil {
				return fmt.Errorf("copying %s: %w", resolved, err)
			}
		}
	}

	return nil
}

// pathWithin returns true if child is equal to or a descendant of parent.
func pathWithin(child string, parent string) bool {
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		resolvedParent = parent
	}
	resolvedChild, err := filepath.EvalSymlinks(child)
	if err != nil {
		resolvedChild = child
	}
	rel, err := filepath.Rel(resolvedParent, resolvedChild)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// copyFile copies a single file from src to dst atomically using temp+rename.
func copyFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source %s: %w", src, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", dst, err)
	}
	tmpName := tmp.Name()

	_, copyErr := io.Copy(tmp, in)
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("copying to %s: %w", dst, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing temp file for %s: %w", dst, closeErr)
	}

	perm := min(info.Mode().Perm(), 0o700)
	if chmodErr := os.Chmod(tmpName, perm); chmodErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("setting permissions on %s: %w", dst, chmodErr)
	}

	if renameErr := os.Rename(tmpName, dst); renameErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming temp to %s: %w", dst, renameErr)
	}

	return nil
}
