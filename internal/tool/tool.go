package tool

import (
	"context"
	"encoding/json"

	"aics/internal/llm"
)

type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Call(ctx context.Context, input json.RawMessage) (string, error)
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry(tools ...Tool) *Registry {
	registry := &Registry{tools: make(map[string]Tool)}
	for _, item := range tools {
		registry.tools[item.Name()] = item
	}
	return registry
}

func (r *Registry) Definitions() []llm.Tool {
	defs := make([]llm.Tool, 0, len(r.tools))
	for _, item := range r.tools {
		defs = append(defs, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        item.Name(),
				Description: item.Description(),
				Parameters:  item.Parameters(),
			},
		})
	}
	return defs
}

func (r *Registry) Call(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
	item, ok := r.tools[name]
	if !ok {
		return "", false, nil
	}
	out, err := item.Call(ctx, input)
	return out, true, err
}
