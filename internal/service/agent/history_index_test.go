package agent

import (
	"strings"
	"testing"
)

// 验证本地 SQLite 真正使用联合索引进行历史定位/排序，不仅检查 schema 声明。
func TestConversationHistoryIndexes(t *testing.T) {
	ctx, client := setupChatTest(t)
	cases := []struct {
		query, index string
		args         []any
	}{
		{`SELECT id FROM kaguya_chat_turn WHERE conversation_id = ? AND turn_index < ? ORDER BY turn_index DESC LIMIT 20`, "kaguyachatturn_conversation_id_turn_index", []any{"123", 100}},
		{`SELECT id FROM kaguya_chat_block WHERE turn_id = ? ORDER BY sequence`, "kaguyachatblock_turn_id_sequence", []any{"123"}},
		{`SELECT id FROM kaguya_conversation WHERE deleted_at IS NULL ORDER BY last_message_at DESC, id DESC LIMIT 20`, "kaguyaconversation_deleted_at_last_message_at_id", nil},
		{`SELECT id FROM kaguya_conversation WHERE deleted_at IS NULL AND favorite = ? ORDER BY last_message_at DESC, id DESC LIMIT 20`, "kaguyaconversation_deleted_at_favorite_last_message_at_id", []any{true}},
	}
	for _, tt := range cases {
		rows, err := client.QueryContext(ctx, "EXPLAIN QUERY PLAN "+tt.query, tt.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan.WriteString(detail)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.String(), tt.index) || strings.Contains(plan.String(), "TEMP B-TREE") {
			t.Fatalf("unexpected query plan for %s: %s", tt.query, plan.String())
		}
	}
}
