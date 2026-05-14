package skill

import (
	"context"
	"encoding/json"
	"fmt"
)

type Tool struct {
	runner *Runner
	skills []Skill
}

type toolInput struct {
	SkillID string          `json:"skill_id"`
	Input   json.RawMessage `json:"input"`
}

func NewTool(runner *Runner, skills []Skill) *Tool {
	return &Tool{runner: runner, skills: skills}
}

func (t *Tool) Name() string {
	return "run_skill"
}

func (t *Tool) Description() string {
	return "运行一个已注册的外部脚本工作流技能。需要实时数据、外部系统、复杂业务流程或脚本处理时使用。"
}

func (t *Tool) Parameters() map[string]any {
	ids := make([]any, 0, len(t.skills))
	for _, item := range t.skills {
		ids = append(ids, item.ID)
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"skill_id": map[string]any{
				"type":        "string",
				"description": "要运行的技能 ID，必须来自系统提示中的可用技能列表。",
				"enum":        ids,
			},
			"input": map[string]any{
				"type":        "object",
				"description": "传给技能的 JSON 参数，字段应符合该技能 parameters 的要求。",
			},
		},
		"required": []string{"skill_id", "input"},
	}
}

func (t *Tool) Call(ctx context.Context, input json.RawMessage) (string, error) {
	var parsed toolInput
	if err := json.Unmarshal(input, &parsed); err != nil {
		return "", err
	}
	if parsed.SkillID == "" {
		return "", fmt.Errorf("skill_id is required")
	}

	var skillInput any
	if len(parsed.Input) > 0 {
		if err := json.Unmarshal(parsed.Input, &skillInput); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
	} else {
		skillInput = map[string]any{}
	}

	result, err := t.runner.Run(ctx, parsed.SkillID, skillInput)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
