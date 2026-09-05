package openai

import "testing"

func TestDefaultModels_ContainsGPT6Family(t *testing.T) {
	t.Parallel()

	byID := make(map[string]Model, len(DefaultModels))
	for _, model := range DefaultModels {
		byID[model.ID] = model
	}

	required := []string{
		"gpt-6",
		"gpt-6-luna",
		"gpt-6-sol",
		"gpt-6-terra",
	}

	for _, id := range required {
		if _, ok := byID[id]; !ok {
			t.Fatalf("expected OpenAI default model %q to be exposed", id)
		}
	}
}
