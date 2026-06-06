package slack

import (
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type slackUserInfo struct {
	name string
	lang string
}

func (p *Platform) setSessionLang(sessionKey, lang string) {
	lang = normalizeSlackLang(lang)
	if sessionKey == "" || lang == "" {
		return
	}
	p.sessionLang.Store(sessionKey, lang)
}

func (p *Platform) langForSession(sessionKey string) string {
	if sessionKey != "" {
		if cached, ok := p.sessionLang.Load(sessionKey); ok {
			if lang, ok := cached.(string); ok && lang != "" {
				return normalizeSlackLang(lang)
			}
		}
	}
	return "en"
}

// rememberSessionLang resolves UI language for a session.
// Priority: explicit /lang → natural-language content → session cache → Slack user locale → default en.
func (p *Platform) rememberSessionLang(sessionKey, userID, content string) string {
	if lang := parseLangCommand(content); lang != "" {
		p.setSessionLang(sessionKey, lang)
		return lang
	}

	content = strings.TrimSpace(content)
	if content != "" && looksLikeNaturalLanguage(content) {
		lang := normalizeSlackLang(string(core.DetectLanguage(content)))
		p.setSessionLang(sessionKey, lang)
		return lang
	}

	if cached, ok := p.sessionLang.Load(sessionKey); ok {
		if lang, ok := cached.(string); ok && lang != "" {
			return normalizeSlackLang(lang)
		}
	}

	if _, lang := p.cachedUserInfo(userID); lang != "" {
		p.setSessionLang(sessionKey, lang)
		return lang
	}

	if content != "" {
		lang := normalizeSlackLang(string(core.DetectLanguage(content)))
		p.setSessionLang(sessionKey, lang)
		return lang
	}

	return "en"
}

func looksLikeNaturalLanguage(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	if !strings.HasPrefix(content, "/") {
		return true
	}
	fields := strings.Fields(content)
	if len(fields) <= 1 {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(content, fields[0])) != ""
}

func (p *Platform) langFromActionValue(sessionKey, rawValue string) string {
	_, _, lang, _ := decodeActionValue(rawValue)
	if lang != "" {
		p.setSessionLang(sessionKey, lang)
		return lang
	}
	return p.langForSession(sessionKey)
}

func (p *Platform) syncLangFromCardAction(sessionKey, actionVal string) {
	if !strings.HasPrefix(actionVal, "act:/lang ") {
		return
	}
	lang := normalizeSlackLang(strings.TrimSpace(strings.TrimPrefix(actionVal, "act:/lang ")))
	if lang != "" {
		p.setSessionLang(sessionKey, lang)
	}
}
