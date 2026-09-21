package feishu

import (
	"fmt"
	"net/mail"
	"strings"
)

// Each mapping is an explicit administrative delegation, not a fallback for
// arbitrary senders without an email. Arrays survive Runtime config rendering.
func parseAutomonJWTDelegations(raw any) (map[string]string, error) {
	result := map[string]string{}
	if raw == nil {
		return result, nil
	}
	var entries []string
	switch values := raw.(type) {
	case []string:
		entries = values
	case []any:
		for _, value := range values {
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("feishu: automon_jwt_delegations must contain strings")
			}
			entries = append(entries, s)
		}
	default:
		return nil, fmt.Errorf("feishu: automon_jwt_delegations must be an array of open_id=email strings")
	}
	for i, entry := range entries {
		id, email, ok := strings.Cut(entry, "=")
		id, email = strings.TrimSpace(id), strings.TrimSpace(email)
		if !ok || !strings.HasPrefix(id, "ou_") || len(id) <= 3 || !isValidFeishuLookupID(id) {
			return nil, fmt.Errorf("feishu: automon_jwt_delegations entry %d requires a valid ou_ open_id", i)
		}
		addr, err := mail.ParseAddress(email)
		if err != nil || addr.Address != email || strings.ContainsAny(email, "\r\n") {
			return nil, fmt.Errorf("feishu: automon_jwt_delegations entry %d requires a plain email address", i)
		}
		if _, exists := result[id]; exists {
			return nil, fmt.Errorf("feishu: duplicate automon_jwt_delegations open_id %q", id)
		}
		result[id] = email
	}
	return result, nil
}
