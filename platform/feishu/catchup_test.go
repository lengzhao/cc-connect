package feishu

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/stretchr/testify/assert"
)

func TestIsBotMentionedInList(t *testing.T) {
	const botID = "ou_bot123"

	t.Run("nil mentions", func(t *testing.T) {
		assert.False(t, isBotMentionedInList(nil, botID))
	})
	t.Run("empty mentions", func(t *testing.T) {
		assert.False(t, isBotMentionedInList([]*larkim.Mention{}, botID))
	})
	t.Run("bot not in list", func(t *testing.T) {
		mentions := []*larkim.Mention{{Id: strPtr("ou_other")}}
		assert.False(t, isBotMentionedInList(mentions, botID))
	})
	t.Run("bot in list", func(t *testing.T) {
		mentions := []*larkim.Mention{
			{Id: strPtr("ou_other")},
			{Id: strPtr(botID)},
		}
		assert.True(t, isBotMentionedInList(mentions, botID))
	})
	t.Run("nil Mention.Id skipped", func(t *testing.T) {
		mentions := []*larkim.Mention{{Id: nil}, {Id: strPtr(botID)}}
		assert.True(t, isBotMentionedInList(mentions, botID))
	})
}

func TestConvertMentions(t *testing.T) {
	t.Run("nil input returns empty slice", func(t *testing.T) {
		assert.Empty(t, convertMentions(nil))
	})
	t.Run("converts open_id string to UserId struct", func(t *testing.T) {
		key, name, openID := "@_user_1", "Alice", "ou_abc"
		result := convertMentions([]*larkim.Mention{{Key: &key, Id: &openID, Name: &name}})
		assert.Len(t, result, 1)
		assert.Equal(t, key, *result[0].Key)
		assert.Equal(t, name, *result[0].Name)
		assert.NotNil(t, result[0].Id)
		assert.Equal(t, openID, *result[0].Id.OpenId)
	})
	t.Run("nil mention entry skipped", func(t *testing.T) {
		id := "ou_x"
		result := convertMentions([]*larkim.Mention{nil, {Id: &id}})
		assert.Len(t, result, 1)
	})
}
