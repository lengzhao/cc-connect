package slack

import "strings"

// mapSlackUserLocale maps Slack users.info locale (e.g. zh-CN, en-US) to engine lang codes.
func mapSlackUserLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return ""
	}
	// Slack uses BCP-47 tags such as en-US, zh-CN, zh-TW, ja-JP, es-ES.
	if i := strings.IndexByte(locale, '-'); i > 0 {
		locale = locale[:i] + strings.ToLower(locale[i:])
	}
	return normalizeSlackLang(locale)
}

func normalizeSlackLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch {
	case l == "zh-tw" || l == "zh-hk" || l == "zhtw":
		return "zh-tw"
	case strings.HasPrefix(l, "zh"):
		return "zh"
	case strings.HasPrefix(l, "ja"), l == "jp":
		return "ja"
	case strings.HasPrefix(l, "es"):
		return "es"
	default:
		return "en"
	}
}

func slackCardLoadingText(lang string) string {
	switch normalizeSlackLang(lang) {
	case "zh":
		return "⏳ 加载中..."
	case "zh-tw":
		return "⏳ 載入中..."
	case "ja":
		return "⏳ 読み込み中..."
	case "es":
		return "⏳ Cargando..."
	default:
		return "⏳ Loading..."
	}
}

func slackAskQuestionSelectedText(lang string) string {
	switch normalizeSlackLang(lang) {
	case "zh":
		return "已选择"
	case "zh-tw":
		return "已選擇"
	case "ja":
		return "選択済み"
	case "es":
		return "Seleccionado"
	default:
		return "Selected"
	}
}

func parseLangCommand(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "/lang") {
		return ""
	}
	fields := strings.Fields(content)
	if len(fields) < 2 {
		return ""
	}
	return normalizeSlackLang(fields[1])
}

