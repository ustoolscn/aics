package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"aics/internal/llm"
)

type Runner struct {
	skills        map[string]Skill
	llm           llm.Client
	defaultMax    int
	scriptTimeout time.Duration
}

type RunResult struct {
	SkillID string         `json:"skill_id"`
	Steps   map[string]any `json:"steps"`
	Final   any            `json:"final,omitempty"`
}

func NewRunner(skills []Skill, llmClient llm.Client, defaultMax int, scriptTimeout time.Duration) *Runner {
	byID := make(map[string]Skill, len(skills))
	for _, item := range skills {
		byID[item.ID] = item
	}
	return &Runner{
		skills:        byID,
		llm:           llmClient,
		defaultMax:    defaultMax,
		scriptTimeout: scriptTimeout,
	}
}

func (r *Runner) Run(ctx context.Context, skillID string, input any) (RunResult, error) {
	item, ok := r.skills[skillID]
	if !ok {
		return RunResult{}, fmt.Errorf("skill %q not found", skillID)
	}
	if len(item.Workflow) == 0 {
		return RunResult{}, fmt.Errorf("skill %q has empty workflow", skillID)
	}

	maxSteps := item.MaxSteps
	if maxSteps <= 0 {
		maxSteps = r.defaultMax
	}
	if maxSteps <= 0 {
		maxSteps = 8
	}

	stepIndex := make(map[string]int, len(item.Workflow))
	for i, step := range item.Workflow {
		if step.ID == "" {
			return RunResult{}, fmt.Errorf("skill %q has workflow step without id", skillID)
		}
		stepIndex[step.ID] = i
	}

	result := RunResult{
		SkillID: item.ID,
		Steps:   make(map[string]any),
	}
	current := 0
	for executed := 0; executed < maxSteps && current < len(item.Workflow); executed++ {
		step := item.Workflow[current]
		var out any
		var next string
		var err error

		switch strings.ToLower(strings.TrimSpace(step.Type)) {
		case "script":
			out, err = r.runScript(ctx, item, step, input, result.Steps)
		case "llm":
			out, next, err = r.runLLMStep(ctx, item, step, input, result.Steps)
		case "end":
			result.Final = result.Steps
			return result, nil
		default:
			err = fmt.Errorf("unknown step type %q", step.Type)
		}
		if err != nil {
			return result, fmt.Errorf("skill %s step %s failed: %w", item.ID, step.ID, err)
		}

		result.Steps[step.ID] = out
		result.Final = out
		if next != "" {
			if next == "finish" || next == "end" {
				return result, nil
			}
			found, ok := stepIndex[next]
			if !ok {
				return result, fmt.Errorf("llm step %s returned unknown next_step %q", step.ID, next)
			}
			current = found
			continue
		}
		current++
	}

	if current < len(item.Workflow) {
		return result, fmt.Errorf("skill %s exceeded max_steps=%d", item.ID, maxSteps)
	}
	return result, nil
}

func (r *Runner) runScript(ctx context.Context, item Skill, step Step, input any, results map[string]any) (any, error) {
	if strings.TrimSpace(step.Entry) == "" {
		return nil, fmt.Errorf("script step missing entry")
	}

	scriptPath := filepath.Clean(filepath.Join(item.dir, step.Entry))
	rel, err := filepath.Rel(item.dir, scriptPath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("script entry escapes skill directory")
	}

	payload := map[string]any{
		"input": input,
		"context": map[string]any{
			"skill_id": item.ID,
			"step_id":  step.ID,
			"results":  results,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	timeout := r.scriptTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(stepCtx, "node", scriptPath)
	cmd.Dir = item.dir
	cmd.Stdin = bytes.NewReader(body)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("node script failed: %s", msg)
	}

	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw), nil
	}
	return decoded, nil
}

func (r *Runner) runLLMStep(ctx context.Context, item Skill, step Step, input any, results map[string]any) (any, string, error) {
	payload := map[string]any{
		"skill": map[string]any{
			"id":          item.ID,
			"name":        item.Name,
			"description": item.Description,
		},
		"input":   input,
		"results": results,
		"choices": step.Choices,
	}
	body, _ := json.MarshalIndent(payload, "", "  ")

	instruction := strings.TrimSpace(step.Instruction)
	if instruction == "" {
		instruction = "根据输入和已有步骤结果，生成结构化 JSON 输出。"
	}
	if len(step.Choices) > 0 {
		instruction += "\n如果需要跳转下一步，只能返回 choices 中的 next_step，或返回 finish 结束。"
	}

	resp, err := r.llm.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: instruction + "\n必须只输出 JSON，不要输出 Markdown。"},
			{Role: llm.RoleUser, Content: string(body)},
		},
	})
	if err != nil {
		return nil, "", err
	}

	text := strings.TrimSpace(llm.ContentString(resp.Message.Content))
	var decoded any
	if err := json.Unmarshal([]byte(stripJSONFence(text)), &decoded); err != nil {
		decoded = text
	}

	next := ""
	if object, ok := decoded.(map[string]any); ok {
		if value, ok := object["next_step"].(string); ok {
			next = strings.TrimSpace(value)
		}
	}
	return decoded, next, nil
}

func stripJSONFence(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return text
	}
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	return strings.TrimSpace(text)
}
