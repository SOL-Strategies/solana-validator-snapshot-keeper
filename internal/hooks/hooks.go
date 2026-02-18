package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"text/template"

	"github.com/charmbracelet/log"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/config"
)

func logger() *log.Logger { return log.Default().WithPrefix("hooks") }

// TemplateData is the data available to hook command templates.
type TemplateData struct {
	SnapshotSlot    string
	SnapshotType    string // "full" or "incremental"
	SourceNode      string
	DownloadTimeSec int
	DownloadSizeMB  int
	SnapshotPath    string
	ClusterName     string
	ValidatorRole   string // "passive" or "unknown"
	Error           string // only populated for on_failure hooks
}

// RunHooks executes a list of hook commands with the given template data.
func RunHooks(ctx context.Context, hooks []config.HookCommand, data TemplateData) error {
	for i, hook := range hooks {
		if hook.Disabled {
			logger().Debug("hook disabled, skipping", "name", hook.Name)
			continue
		}

		logger().Info("running hook", "name", hook.Name, "index", i)

		if err := runHook(ctx, hook, data); err != nil {
			if hook.AllowFailure {
				logger().Warn("hook failed (allow_failure=true)", "name", hook.Name, "error", err)
				continue
			}
			return fmt.Errorf("hook %q failed: %w", hook.Name, err)
		}

		logger().Info("hook completed", "name", hook.Name)
	}
	return nil
}

func runHook(ctx context.Context, hook config.HookCommand, data TemplateData) error {
	cmd, err := renderTemplate(hook.Cmd, data)
	if err != nil {
		return fmt.Errorf("rendering cmd template: %w", err)
	}

	var args []string
	for _, arg := range hook.Args {
		rendered, err := renderTemplate(arg, data)
		if err != nil {
			return fmt.Errorf("rendering arg template: %w", err)
		}
		args = append(args, rendered)
	}

	execCmd := exec.CommandContext(ctx, cmd, args...)

	// Set environment variables
	for k, v := range hook.Environment {
		rendered, err := renderTemplate(v, data)
		if err != nil {
			return fmt.Errorf("rendering env %q template: %w", k, err)
		}
		execCmd.Env = append(execCmd.Env, fmt.Sprintf("%s=%s", k, rendered))
	}

	if hook.StreamOutput {
		execCmd.Stdout = &logWriter{prefix: hook.Name, level: "info"}
		execCmd.Stderr = &logWriter{prefix: hook.Name, level: "error"}
		return execCmd.Run()
	}

	output, err := execCmd.CombinedOutput()
	if err != nil {
		logger().Error("hook output", "name", hook.Name, "output", string(output))
		return err
	}
	if len(output) > 0 {
		logger().Debug("hook output", "name", hook.Name, "output", string(output))
	}
	return nil
}

func renderTemplate(tmplStr string, data TemplateData) (string, error) {
	tmpl, err := template.New("").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// logWriter implements io.Writer and logs each line.
type logWriter struct {
	prefix string
	level  string
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	msg := string(bytes.TrimSpace(p))
	if msg == "" {
		return len(p), nil
	}
	if w.level == "error" {
		logger().Error(msg, "hook", w.prefix)
	} else {
		logger().Info(msg, "hook", w.prefix)
	}
	return len(p), nil
}
