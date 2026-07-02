package server

import (
	"testing"

	"aigis/internal/core/usage"
)

func TestApplyUsageTokens(t *testing.T) {
	tests := []struct {
		name                  string
		resp                  string
		wantPrompt, wantCompl int
		wantTotal             int
	}{
		{
			name:       "openai shape with total",
			resp:       `{"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
			wantPrompt: 11, wantCompl: 7, wantTotal: 18,
		},
		{
			name:       "anthropic shape without total falls back to sum",
			resp:       `{"usage":{"input_tokens":30,"output_tokens":12}}`,
			wantPrompt: 30, wantCompl: 12, wantTotal: 42,
		},
		{
			name:       "openai without total falls back to sum",
			resp:       `{"usage":{"prompt_tokens":5,"completion_tokens":5}}`,
			wantPrompt: 5, wantCompl: 5, wantTotal: 10,
		},
		{
			name:       "no usage block yields zeros",
			resp:       `{"choices":[]}`,
			wantPrompt: 0, wantCompl: 0, wantTotal: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e usage.Event
			applyUsageTokens(&e, []byte(tt.resp))
			if e.PromptTokens != tt.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", e.PromptTokens, tt.wantPrompt)
			}
			if e.CompletionTokens != tt.wantCompl {
				t.Errorf("CompletionTokens = %d, want %d", e.CompletionTokens, tt.wantCompl)
			}
			if e.TotalTokens != tt.wantTotal {
				t.Errorf("TotalTokens = %d, want %d", e.TotalTokens, tt.wantTotal)
			}
		})
	}
}
