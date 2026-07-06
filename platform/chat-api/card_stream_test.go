package chatapi

import "testing"

func TestParseStreamingCardContent(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		thinking string
		answer   string
	}{
		{
			name:     "answer only",
			content:  "hello world",
			thinking: "",
			answer:   "hello world",
		},
		{
			name:     "thinking only",
			content:  streamThinkingHeader + "planning" + streamSectionBreak,
			thinking: "planning",
			answer:   "",
		},
		{
			name: "thinking and answer",
			content: streamThinkingHeader + "planning" + streamSectionBreak +
				streamSectionBreak + "final answer",
			thinking: "planning",
			answer:   "final answer",
		},
		{
			name: "tool then answer",
			content: "🔧 **Tool #1**: `bash`\nls\n\n" + streamSectionBreak + "done",
			thinking: "",
			answer:   "done",
		},
		{
			name: "engine layout golden",
			content: streamThinkingHeader + "Inspecting repo" + streamSectionBreak +
				streamSectionBreak + "Here is the summary",
			thinking: "Inspecting repo",
			answer:   "Here is the summary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotThink, gotAns := parseStreamingCardContent(tt.content)
			if gotThink != tt.thinking || gotAns != tt.answer {
				t.Fatalf("parse(%q) = (%q, %q), want (%q, %q)", tt.content, gotThink, gotAns, tt.thinking, tt.answer)
			}
		})
	}
}
