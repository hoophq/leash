package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const hookCommand = "leash hook claude-code"

// toolMatcher is the Claude Code tool-name regexp Leash hooks into: the tools
// whose actions the engine actually evaluates.
const toolMatcher = "Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch"

func newInitCommand() *cobra.Command {
	var global bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install the Leash hook into Claude Code settings",
		Long: "Adds a PreToolUse hook to your Claude Code settings so Leash inspects\n" +
			"each tool call. By default it writes the project settings\n" +
			"(./.claude/settings.json); use --global for ~/.claude/settings.json.\n\n" +
			"The change is idempotent and preserves any existing settings.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := settingsPath(global)
			if err != nil {
				return fail(cmd, err)
			}
			result, err := installHook(path, hookInvocation())
			if err != nil {
				return fail(cmd, err)
			}
			switch result {
			case hookInstalled:
				fmt.Fprintf(cmd.OutOrStdout(), "Installed Leash hook in %s\n", path)
				fmt.Fprintf(cmd.OutOrStdout(), "Restart Claude Code (or start a new session) to activate it.\n")
			case hookUpdated:
				fmt.Fprintf(cmd.OutOrStdout(), "Updated the Leash hook command in %s\n", path)
				fmt.Fprintf(cmd.OutOrStdout(), "Restart Claude Code (or start a new session) to pick it up.\n")
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "Leash hook already present in %s\n", path)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&global, "global", false, "install into ~/.claude/settings.json instead of the project")
	return cmd
}

func settingsPath(global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude", "settings.json"), nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, ".claude", "settings.json"), nil
}

// hookInvocation returns the command string Claude Code should run. It uses the
// absolute path of the current binary so the hook works regardless of PATH, but
// keeps symlinks unresolved: package managers point a stable symlink (e.g.
// /opt/homebrew/bin/leash) at a version-pinned target that vanishes on upgrade,
// so resolving it would break the hook at the next `brew upgrade`.
func hookInvocation() string {
	exe, err := os.Executable()
	if err != nil {
		return hookCommand // fall back to PATH lookup of "leash"
	}
	return fmt.Sprintf("%s hook claude-code", exe)
}

// hookInstallResult describes what installHook changed.
type hookInstallResult int

const (
	hookUnchanged hookInstallResult = iota
	hookInstalled
	hookUpdated
)

// installHook merges a PreToolUse hook into the settings file at path, creating
// it if necessary. An existing leash hook whose command differs — e.g. a stale
// binary path left by a previous install — is updated in place, so re-running
// `leash init` always converges on a working hook.
func installHook(path, command string) (hookInstallResult, error) {
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) > 0 {
			if err := json.Unmarshal(data, &settings); err != nil {
				return hookUnchanged, fmt.Errorf("%s is not valid JSON: %w", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return hookUnchanged, err
	}

	hooks := asMap(settings["hooks"])
	preToolUse := asSlice(hooks["PreToolUse"])

	result := hookUnchanged
	for _, e := range preToolUse {
		for _, h := range asSlice(asMap(e)["hooks"]) {
			hm := asMap(h)
			cmd, ok := hm["command"].(string)
			if !ok || !containsHook(cmd) {
				continue
			}
			if cmd != command {
				hm["command"] = command
				return writeSettings(path, settings, hooks, preToolUse, hookUpdated)
			}
			result = hookInstalled // present and current
		}
	}
	if result == hookInstalled {
		return hookUnchanged, nil
	}

	entry := map[string]any{
		"matcher": toolMatcher,
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	}
	preToolUse = append(preToolUse, entry)
	return writeSettings(path, settings, hooks, preToolUse, hookInstalled)
}

func writeSettings(path string, settings, hooks map[string]any, preToolUse []any, result hookInstallResult) (hookInstallResult, error) {
	hooks["PreToolUse"] = preToolUse
	settings["hooks"] = hooks

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return hookUnchanged, err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return hookUnchanged, err
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return hookUnchanged, err
	}
	return result, nil
}

func containsHook(cmd string) bool {
	return len(cmd) >= len(hookCommand) && (cmd == hookCommand ||
		// match "<path>/leash hook claude-code" too
		hasSuffix(cmd, hookCommand))
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}
