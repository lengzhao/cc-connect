package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// Matches the official larksuite/cli Card 2.0 multi_select_static contract.
// Check the encoded card itself, including the HTTP PATCH used on later steps.
func assertMultiSelectDefault(t *testing.T, card map[string]any) {
	t.Helper()
	var visit func(any)
	count := 0
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if v["tag"] == "multi_select_static" {
				count++
				if _, bad := v["initial_options"]; bad {
					t.Error("Card 2.0 rejects initial_options")
				}
				selected, ok := v["selected_values"].([]any)
				if !ok || len(selected) != 1 || selected[0] != "ipa" {
					t.Errorf("wrong selected_values: %#v", v["selected_values"])
				}
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(card)
	if count != 1 {
		t.Fatalf("expected one multi-select, got %d", count)
	}
}
func TestDecisionMultiSelectDefaultsCard2Wire(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{{"tool_json_array", []any{"ipa"}}, {"normalized_saved_values", []string{"ipa"}}} {
		t.Run(tc.name, func(t *testing.T) {
			v := &core.Decision{ID: "form", MessageID: "om_original", Revision: 1, Spec: core.DecisionSpec{Title: "Build", Markdown: "Check", Fields: []core.DecisionField{{ID: "platforms", Label: "Platforms", Type: "multiselect", Default: tc.value, Options: []core.DecisionOption{{ID: "ipa", Label: "IPA"}, {ID: "apk", Label: "APK"}}}}, Options: []core.DecisionOption{{ID: "check", Label: "Check", Intermediate: true}}}}
			for _, restore := range []bool{false, true} {
				if restore {
					data, _ := json.Marshal(v)
					var saved core.Decision
					if err := json.Unmarshal(data, &saved); err != nil {
						t.Fatal(err)
					}
					v = &saved
				}
				data, _ := json.Marshal(decisionCard(v, false))
				var wire map[string]any
				_ = json.Unmarshal(data, &wire)
				assertMultiSelectDefault(t, wire)
			}
			patches := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					_, _ = w.Write([]byte(`{"code":0,"expire":7200,"tenant_access_token":"test"}`))
					return
				}
				if r.Method != "PATCH" || r.URL.Path != "/open-apis/im/v1/messages/om_original" {
					t.Errorf("wrong update target %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					Content string `json:"content"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				var wire map[string]any
				if err := json.Unmarshal([]byte(body.Content), &wire); err != nil {
					t.Error(err)
				}
				assertMultiSelectDefault(t, wire)
				patches++
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer srv.Close()
			p := &Platform{client: lark.NewClient("card-default-"+tc.name, "test", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))}
			if err := p.UpdateDecision(context.Background(), v); err != nil {
				t.Fatal(err)
			}
			if patches != 1 {
				t.Fatalf("expected one original-card update, got %d", patches)
			}
		})
	}
}
