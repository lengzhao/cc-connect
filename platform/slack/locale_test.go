package slack

import "testing"

func TestSlackCardLoadingText(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"en", "⏳ Loading..."},
		{"zh", "⏳ 加载中..."},
		{"zh-TW", "⏳ 載入中..."},
		{"ja", "⏳ 読み込み中..."},
		{"es", "⏳ Cargando..."},
	}
	for _, tt := range tests {
		if got := slackCardLoadingText(tt.lang); got != tt.want {
			t.Fatalf("slackCardLoadingText(%q) = %q, want %q", tt.lang, got, tt.want)
		}
	}
}

func TestMapSlackUserLocale(t *testing.T) {
	tests := map[string]string{
		"en-US": "en",
		"zh-CN": "zh",
		"zh-TW": "zh-tw",
		"ja-JP": "ja",
		"es-ES": "es",
		"":      "",
	}
	for locale, want := range tests {
		if got := mapSlackUserLocale(locale); got != want {
			t.Fatalf("mapSlackUserLocale(%q) = %q, want %q", locale, got, want)
		}
	}
}

func TestRememberSessionLang(t *testing.T) {
	p := &Platform{}
	key := "slack:C:U"
	if got := p.rememberSessionLang(key, "U1", "你好"); got != "zh" {
		t.Fatalf("detect zh = %q", got)
	}
	if got := p.langForSession(key); got != "zh" {
		t.Fatalf("cached lang = %q", got)
	}
	if got := p.rememberSessionLang(key, "U1", "/lang en"); got != "en" {
		t.Fatalf("lang command = %q", got)
	}
}

func TestRememberSessionLangUsesSlackLocaleForSlashCommand(t *testing.T) {
	p := &Platform{}
	p.userInfoCache.Store("U1", slackUserInfo{name: "Alice", lang: "zh"})
	key := "slack:C:U1"
	if got := p.rememberSessionLang(key, "U1", "/help"); got != "zh" {
		t.Fatalf("slash command with zh locale = %q, want zh", got)
	}
}

func TestSyncLangFromCardAction(t *testing.T) {
	p := &Platform{}
	key := "slack:C:U"
	p.syncLangFromCardAction(key, "act:/lang ja")
	if got := p.langForSession(key); got != "ja" {
		t.Fatalf("lang = %q, want ja", got)
	}
}
