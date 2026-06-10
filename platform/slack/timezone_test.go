package slack

import "testing"

func TestUserTimezoneFromCache(t *testing.T) {
	p := &Platform{}
	p.userInfoCache.Store("U222", slackUserInfo{name: "Alice", timezone: "Asia/Shanghai"})

	if got := p.UserTimezone("U222"); got != "Asia/Shanghai" {
		t.Fatalf("UserTimezone() = %q, want Asia/Shanghai", got)
	}
}

func TestUserTimezoneEmptyUserID(t *testing.T) {
	p := &Platform{}
	if got := p.UserTimezone(""); got != "" {
		t.Fatalf("UserTimezone('') = %q, want empty", got)
	}
}
