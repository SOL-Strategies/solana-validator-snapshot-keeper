package hooks

import (
	"context"
	"testing"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/config"
)

func TestRunHooks_Success(t *testing.T) {
	hooks := []config.HookCommand{
		{
			Name: "test-echo",
			Cmd:  "echo",
			Args: []string{"slot={{ .SnapshotSlot }}"},
		},
	}

	data := TemplateData{
		SnapshotSlot: "135501350",
	}

	err := RunHooks(context.Background(), hooks, data)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunHooks_Disabled(t *testing.T) {
	hooks := []config.HookCommand{
		{
			Name:     "disabled-hook",
			Cmd:      "false", // would fail if run
			Disabled: true,
		},
	}

	err := RunHooks(context.Background(), hooks, TemplateData{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunHooks_AllowFailure(t *testing.T) {
	hooks := []config.HookCommand{
		{
			Name:         "failing-hook",
			Cmd:          "false",
			AllowFailure: true,
		},
		{
			Name: "after-failure",
			Cmd:  "echo",
			Args: []string{"still runs"},
		},
	}

	err := RunHooks(context.Background(), hooks, TemplateData{})
	if err != nil {
		t.Fatal("should not error when allow_failure=true")
	}
}

func TestRunHooks_FailureAborts(t *testing.T) {
	hooks := []config.HookCommand{
		{
			Name:         "failing-hook",
			Cmd:          "false",
			AllowFailure: false,
		},
		{
			Name: "should-not-run",
			Cmd:  "echo",
			Args: []string{"should not reach here"},
		},
	}

	err := RunHooks(context.Background(), hooks, TemplateData{})
	if err == nil {
		t.Error("expected error when hook fails with allow_failure=false")
	}
}

func TestRenderTemplate(t *testing.T) {
	data := TemplateData{
		SnapshotSlot: "12345",
		SourceNode:   "10.0.0.1:8899",
		Error:        "connection refused",
	}

	tests := []struct {
		tmpl     string
		expected string
	}{
		{"slot={{ .SnapshotSlot }}", "slot=12345"},
		{"node={{ .SourceNode }}", "node=10.0.0.1:8899"},
		{"error={{ .Error }}", "error=connection refused"},
		{"static text", "static text"},
	}

	for _, tt := range tests {
		got, err := renderTemplate(tt.tmpl, data)
		if err != nil {
			t.Errorf("renderTemplate(%q): %v", tt.tmpl, err)
			continue
		}
		if got != tt.expected {
			t.Errorf("renderTemplate(%q) = %q, want %q", tt.tmpl, got, tt.expected)
		}
	}
}

func TestRunHooks_StreamOutput(t *testing.T) {
	hooks := []config.HookCommand{
		{
			Name:         "stream-test",
			Cmd:          "echo",
			Args:         []string{"streamed output"},
			StreamOutput: true,
		},
	}

	err := RunHooks(context.Background(), hooks, TemplateData{})
	if err != nil {
		t.Fatal(err)
	}
}
