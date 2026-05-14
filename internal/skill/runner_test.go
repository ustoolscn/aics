package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aics/internal/llm"
)

type fakeLLM struct{}

func (fakeLLM) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "{}"}}, nil
}

func TestRunnerUsesSkillDirectoryWithoutDuplicatingRelativePath(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	stepDir := filepath.Join(skillsDir, "demo", "steps")
	if err := os.MkdirAll(stepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "demo", "skill.json"), []byte(`{
		"id": "demo",
		"name": "Demo",
		"description": "Demo skill",
		"workflow": [{"id": "run", "type": "script", "entry": "steps/run.js"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stepDir, "run.js"), []byte(`console.log(JSON.stringify({ ok: true }))`), 0o600); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	loaded, err := NewLoader("skills").LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(loaded, fakeLLM{}, 4, 5*time.Second)
	result, err := runner.Run(ctx, "demo", map[string]any{})
	if err != nil {
		if strings.Contains(err.Error(), "Cannot find module") {
			t.Fatalf("script path was resolved relative to the skill dir twice: %v", err)
		}
		t.Fatal(err)
	}
	if result.SkillID != "demo" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
