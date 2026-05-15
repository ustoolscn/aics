package llm

import "testing"

func TestStreamToolCallAccumulator(t *testing.T) {
	acc := newStreamToolCallAccumulator()

	acc.Add([]struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}{
		{
			Index: 0,
			ID:    "call-1",
			Type:  "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "knowledge_search", Arguments: `{"query":"hel`},
		},
		{
			Index: 0,
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Arguments: `lo"}`},
		},
	})

	got := acc.List()
	if len(got) != 1 {
		t.Fatalf("expected one tool call, got %#v", got)
	}
	if got[0].ID != "call-1" || got[0].Type != "function" {
		t.Fatalf("unexpected tool call metadata: %#v", got[0])
	}
	if got[0].Function.Name != "knowledge_search" {
		t.Fatalf("unexpected function name: %q", got[0].Function.Name)
	}
	if got[0].Function.Arguments != `{"query":"hello"}` {
		t.Fatalf("unexpected arguments: %q", got[0].Function.Arguments)
	}
}
