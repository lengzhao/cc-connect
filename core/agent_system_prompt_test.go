package core

import (
	"strings"
	"testing"
)

func TestAgentSystemPrompt_TimerDisabledOmitsTimerSection(t *testing.T) {
	SetTimerFeatureEnabled(false)
	defer SetTimerFeatureEnabled(true)

	prompt := AgentSystemPrompt()
	if strings.Contains(prompt, "cc-connect timer") {
		t.Fatal("prompt should not mention cc-connect timer when feature disabled")
	}
	if strings.Contains(prompt, "/timer") {
		t.Fatal("prompt should not mention /timer when feature disabled")
	}
	if !strings.Contains(prompt, "cc-connect cron") {
		t.Fatal("prompt should still document cron when timer disabled")
	}
	if !strings.Contains(prompt, "NOT available") {
		t.Fatal("prompt should state one-shot delays are unavailable when timer disabled")
	}
}

func TestAgentSystemPrompt_TimerEnabledIncludesTimerSection(t *testing.T) {
	SetTimerFeatureEnabled(true)

	prompt := AgentSystemPrompt()
	if !strings.Contains(prompt, "cc-connect timer add") {
		t.Fatal("prompt should document timer CLI when feature enabled")
	}
	if !strings.Contains(prompt, "/cron vs /timer") {
		t.Fatal("prompt should compare cron vs timer when feature enabled")
	}
}

func TestTimerFeatureEnabled_DefaultTrue(t *testing.T) {
	SetTimerFeatureEnabled(true)
	if !TimerFeatureEnabled() {
		t.Fatal("TimerFeatureEnabled should default to true")
	}
}
