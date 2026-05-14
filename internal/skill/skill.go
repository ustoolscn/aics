package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Skill struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Workflow    []Step         `json:"workflow"`
	MaxSteps    int            `json:"max_steps,omitempty"`
	dir         string
}

type Step struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Entry       string   `json:"entry,omitempty"`
	Instruction string   `json:"instruction,omitempty"`
	Choices     []string `json:"choices,omitempty"`
}

type Loader struct {
	dir string
}

func NewLoader(dir string) *Loader {
	return &Loader{dir: dir}
}

func (l *Loader) LoadAll() ([]Skill, error) {
	root, err := filepath.Abs(l.dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "skill.json")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var item Skill
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, fmt.Errorf("load skill %s: %w", path, err)
		}
		if strings.TrimSpace(item.ID) == "" {
			return nil, fmt.Errorf("load skill %s: missing id", path)
		}
		item.dir = filepath.Dir(path)
		skills = append(skills, item)
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].ID < skills[j].ID
	})
	return skills, nil
}

func BuildIndexPrompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	type indexItem struct {
		ID          string         `json:"id"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	}

	items := make([]indexItem, 0, len(skills))
	for _, item := range skills {
		items = append(items, indexItem{
			ID:          item.ID,
			Name:        item.Name,
			Description: item.Description,
			Parameters:  item.Parameters,
		})
	}

	data, _ := json.MarshalIndent(items, "", "  ")
	return "可用技能列表如下。用户问题需要实时数据、外部系统、复杂业务流程或本地脚本处理时，调用 run_skill 工具；不需要技能时直接回复。不要编造实时数据。\n\n" + string(data)
}
