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
			name:     "tool then answer",
			content:  "🔧 **Tool #1**: `bash`\nls\n\n" + streamSectionBreak + "done",
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
		{
			name: "tools only yield empty answer",
			content: streamThinkingHeader + "plan" + streamSectionBreak +
				"🔧 **Tool #1**: `Bash`\n```bash\ndate\n```\n\n",
			thinking: "plan",
			answer:   "",
		},
		{
			name: "answer with markdown horizontal rules preserved",
			content: streamThinkingHeader + "fetch quote" + streamSectionBreak +
				"---\n\n正在获取 ETH/USDT 实时行情。\n\n---\n\n## 策略\n表格\n\n---\n\n## 风险\n\n需要我进一步细化吗？",
			thinking: "fetch quote",
			answer:   "正在获取 ETH/USDT 实时行情。\n\n---\n\n## 策略\n表格\n\n---\n\n## 风险\n\n需要我进一步细化吗？",
		},
		{
			name: "thinking tool and answer with dividers",
			content: streamThinkingHeader + "need market" + streamSectionBreak +
				"🔧 **Tool #1**: `Bash`\n```bash\ncurl quote\n```\n\n" +
				"---\n\n行情摘要\n\n---\n\n执行建议\n\n---\n\n风险提示",
			thinking: "need market",
			answer:   "行情摘要\n\n---\n\n执行建议\n\n---\n\n风险提示",
		},
		{
			name: "code fence containing --- line kept in answer",
			content: streamThinkingHeader + "draft" + streamSectionBreak +
				"---\n\n```md\nA\n---\nB\n```",
			thinking: "draft",
			answer:   "```md\nA\n---\nB\n```",
		},
		{
			name: "multiple tool blocks then answer with dividers",
			content: "🔧 **Tool #1**: `Bash`\n`one`\n\n" +
				"🔧 **Tool #2**: `Read`\n`/tmp/a`\n\n" +
				streamSectionBreak +
				"step1\n\n---\n\nstep2\n\n---\n\nstep3",
			thinking: "",
			answer:   "step1\n\n---\n\nstep2\n\n---\n\nstep3",
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

func TestExtractStreamingToolCalls(t *testing.T) {
	content := streamThinkingHeader + "need time" + streamSectionBreak +
		"🔧 **Tool #1**: `Bash`\n```bash\ndate\n```\n\n" +
		"🔧 **Tool #2**: `Read`\n`/tmp/a.txt`\n\n" +
		streamSectionBreak + "done"
	got := extractStreamingToolCalls(content)
	if len(got) != 2 {
		t.Fatalf("got %d tools: %+v", len(got), got)
	}
	if got[0].ID != "1" || got[0].Name != "Bash" || got[0].Input != "date" {
		t.Fatalf("tool0 = %+v", got[0])
	}
	if got[1].ID != "2" || got[1].Name != "Read" || got[1].Input != "/tmp/a.txt" {
		t.Fatalf("tool1 = %+v", got[1])
	}
}

func TestParseToolResultFallback(t *testing.T) {
	raw := "🧾 Bash\n🟢 状态: ok\n🔢 退出码: 0\n```text\n2026年 7月15日\n```"
	got, ok := parseToolResultFallback(raw)
	if !ok {
		t.Fatal("expected match")
	}
	if got.Name != "Bash" || got.Status != "ok" || got.Output != "2026年 7月15日" {
		t.Fatalf("got = %+v", got)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("exit = %v", got.ExitCode)
	}
	if got.Success == nil || !*got.Success {
		t.Fatalf("success = %v", got.Success)
	}

	emptyOut := "🧾\n🟢 Status: ok\n_No output_"
	got2, ok := parseToolResultFallback(emptyOut)
	if !ok || got2.Output != "" || got2.Status != "ok" {
		t.Fatalf("empty output parse = %+v ok=%v", got2, ok)
	}

	if _, ok := parseToolResultFallback("normal reply"); ok {
		t.Fatal("plain text must not match")
	}
}
