package claudecode

import "strings"

const skillGuideTrainingRoutePrefix = `[cc-connect training-route]
@skill-guide with Automon training context must run in the skill-guide subagent, not the main agent.
Your only allowed first action is one Agent tool call with subagent_type=skill-guide, passing the full user message unchanged.
Do not answer training questions yourself and do not call training.* MCP tools in the main agent.

`

// prependSkillGuideTrainingRoute forces main-agent delegation when Training Station
// prefixes a turn with @skill-guide and LTS injects the training bootstrap block.
// Plain @skill-guide without training context is left unchanged.
func prependSkillGuideTrainingRoute(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return prompt
	}
	if !strings.Contains(trimmed, "[Authoritative Automon training context]") {
		return prompt
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "@skill-guide") {
		return prompt
	}
	if strings.HasPrefix(trimmed, strings.TrimSpace(skillGuideTrainingRoutePrefix)) {
		return prompt
	}
	return skillGuideTrainingRoutePrefix + prompt
}
