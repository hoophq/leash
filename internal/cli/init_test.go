package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const wantCommand = "/opt/homebrew/bin/leash hook claude-code"

// hookCommands walks hooks.PreToolUse and returns every hook command string.
func hookCommands(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings not valid JSON: %v", err)
	}
	var cmds []string
	for _, e := range asSlice(asMap(settings["hooks"])["PreToolUse"]) {
		for _, h := range asSlice(asMap(e)["hooks"]) {
			if cmd, ok := asMap(h)["command"].(string); ok {
				cmds = append(cmds, cmd)
			}
		}
	}
	return cmds
}

func TestInstallHook(t *testing.T) {
	tests := []struct {
		name    string
		initial string // "" means the settings file does not exist yet
		want    hookInstallResult
		cmds    []string // expected hook commands after the call, in order
	}{
		{
			name: "creates settings file and parent dirs",
			want: hookInstalled,
			cmds: []string{wantCommand},
		},
		{
			name: "appends alongside unrelated hooks",
			initial: `{"permissions":{"allow":["Bash(ls *)"]},
				"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo hi"}]}]}}`,
			want: hookInstalled,
			cmds: []string{"echo hi", wantCommand},
		},
		{
			name: "idempotent when the command already matches",
			initial: `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[
				{"type":"command","command":"/opt/homebrew/bin/leash hook claude-code"}]}]}}`,
			want: hookUnchanged,
			cmds: []string{wantCommand},
		},
		{
			// The brew-cask trap: a previous init resolved the symlink into a
			// version-pinned Caskroom path that no longer exists after upgrade.
			// Re-running init must heal it, not report "already present".
			name: "heals a stale binary path",
			initial: `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[
				{"type":"command","command":"/opt/homebrew/Caskroom/leash/0.0.2/leash hook claude-code"}]}]}}`,
			want: hookUpdated,
			cmds: []string{wantCommand},
		},
		{
			name:    "bare PATH command is recognized and healed to absolute",
			initial: `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"leash hook claude-code"}]}]}}`,
			want:    hookUpdated,
			cmds:    []string{wantCommand},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".claude", "settings.json")
			if tt.initial != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tt.initial), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := installHook(path, wantCommand)
			if err != nil {
				t.Fatalf("installHook: %v", err)
			}
			if got != tt.want {
				t.Fatalf("result = %v, want %v", got, tt.want)
			}

			cmds := hookCommands(t, path)
			if len(cmds) != len(tt.cmds) {
				t.Fatalf("hook commands = %q, want %q", cmds, tt.cmds)
			}
			for i := range cmds {
				if cmds[i] != tt.cmds[i] {
					t.Fatalf("hook command[%d] = %q, want %q", i, cmds[i], tt.cmds[i])
				}
			}
		})
	}
}

func TestInstallHookPreservesUnrelatedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	initial := `{"permissions":{"allow":["Bash(ls *)"]},"model":"opus"}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := installHook(path, wantCommand); err != nil {
		t.Fatalf("installHook: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["model"] != "opus" {
		t.Errorf("model = %v, want opus", settings["model"])
	}
	allow := asSlice(asMap(settings["permissions"])["allow"])
	if len(allow) != 1 || allow[0] != "Bash(ls *)" {
		t.Errorf("permissions.allow = %v, want [Bash(ls *)]", allow)
	}
}

func TestInstallHookUnchangedDoesNotRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// Deliberately not in MarshalIndent formatting: a rewrite would reformat.
	initial := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` + wantCommand + `"}]}]}}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := installHook(path, wantCommand); err != nil || got != hookUnchanged {
		t.Fatalf("installHook = %v, %v; want hookUnchanged, nil", got, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != initial {
		t.Errorf("file was rewritten despite no change:\n%s", data)
	}
}

func TestInstallHookRejectsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installHook(path, wantCommand); err == nil {
		t.Fatal("expected an error for invalid JSON, got nil")
	}
}

func TestHookInvocationKeepsSymlinksUnresolved(t *testing.T) {
	got := hookInvocation()
	if !strings.HasSuffix(got, " hook claude-code") {
		t.Fatalf("hookInvocation() = %q, want suffix %q", got, " hook claude-code")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	// The invocation must use the executable path as-is — resolving symlinks is
	// what pinned brew users to a Caskroom path that dies on upgrade.
	if want := exe + " hook claude-code"; got != want {
		t.Errorf("hookInvocation() = %q, want %q", got, want)
	}
}

func TestContainsHook(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"leash hook claude-code", true},
		{"/opt/homebrew/bin/leash hook claude-code", true},
		{"/Users/dev/go/bin/leash hook claude-code", true},
		{"leash check 'rm -rf ~'", false},
		{"echo leash hook claude-code | wc -l", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := containsHook(tt.cmd); got != tt.want {
			t.Errorf("containsHook(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}
