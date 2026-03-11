package feed

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ExecHook represents a single hook configuration that executes commands
// after specific lifecycle events (e.g., episode downloads).
//
// Example configuration:
//
//	[[feeds.ID1.post_episode_download]]
//	command = ["curl", "-X", "POST", "-d", "$EPISODE_TITLE", "webhook.example.com"]
//	timeout = 30
//
// Environment variables available to hooks:
//   - EPISODE_FILE: Path to downloaded file (e.g., "podcast-id/episode.mp3")
//   - FEED_NAME: The feed identifier
//   - EPISODE_TITLE: The episode title
type ExecHook struct {
	// Command is the command and arguments to execute.
	// For single commands, use shell parsing: ["echo hello"]
	// For multiple args, pass directly: ["curl", "-X", "POST", "url"]
	Command []string `toml:"command"`

	// Shell enables explicit shell execution for single-string commands.
	// Supported values: "", "sh", "cmd", "powershell", "pwsh", "none".
	Shell string `toml:"shell"`

	// Timeout in seconds for command execution.
	// If 0 or unset, defaults to 60 seconds.
	Timeout int `toml:"timeout"`
}

// Invoke executes the hook command with the provided environment variables.
//
// The method handles nil hooks gracefully (returns nil) and validates that
// the command is not empty. Commands are executed with a timeout (default 60s)
// and inherit the parent process environment plus any additional variables.
//
// Single-element commands are executed via shell (/bin/sh -c), while
// multi-element commands are executed directly for better security.
//
// Returns an error if the command fails, times out, or returns a non-zero exit code.
// The error includes the combined stdout/stderr output for debugging.
func (h *ExecHook) Invoke(env []string) error {
	if h == nil {
		return nil
	}
	if len(h.Command) == 0 {
		return fmt.Errorf("hook command is empty")
	}

	// Set up context with timeout (default 1 minute if not specified)
	timeout := h.Timeout
	if timeout == 0 {
		timeout = 60 // default to 1 minute
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd, err := h.buildCommand(ctx)
	if err != nil {
		return err
	}

	// Set up environment variables
	cmd.Env = append(os.Environ(), env...)

	// Execute the command
	data, err := cmd.CombinedOutput()
	output := string(data)

	if err != nil {
		return fmt.Errorf("hook execution failed: %v, output: %s", err, output)
	}

	return nil
}

func (h *ExecHook) buildCommand(ctx context.Context) (*exec.Cmd, error) {
	shell := strings.ToLower(strings.TrimSpace(h.Shell))
	if shell == "none" {
		shell = ""
	}

	if len(h.Command) > 1 {
		if shell != "" {
			return nil, fmt.Errorf("hook shell %q cannot be used with multi-argument direct command", h.Shell)
		}
		return exec.CommandContext(ctx, h.Command[0], h.Command[1:]...), nil
	}

	command := strings.TrimSpace(h.Command[0])
	if command == "" {
		return nil, fmt.Errorf("hook command is empty")
	}

	if shell == "" && !looksLikeShellCommand(command) {
		return exec.CommandContext(ctx, command), nil
	}

	name, args, err := shellCommand(shell, command)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, name, args...), nil
}

func looksLikeShellCommand(command string) bool {
	return strings.ContainsAny(command, "|&;<>()$`\"'*!?[]{}=") || strings.Contains(command, " ") || strings.Contains(command, "\t")
}

func shellCommand(shell, command string) (string, []string, error) {
	switch shell {
	case "", "sh":
		if runtime.GOOS == "windows" {
			if shell == "sh" {
				return "", nil, fmt.Errorf("hook shell %q is not supported on %s", shell, runtime.GOOS)
			}
			return "cmd.exe", []string{"/C", command}, nil
		}
		return "/bin/sh", []string{"-c", command}, nil
	case "cmd":
		return "cmd.exe", []string{"/C", command}, nil
	case "powershell":
		return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", command}, nil
	case "pwsh":
		return "pwsh", []string{"-NoProfile", "-NonInteractive", "-Command", command}, nil
	default:
		return "", nil, fmt.Errorf("unsupported hook shell %q", shell)
	}
}
