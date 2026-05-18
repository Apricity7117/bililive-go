package danmaku

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
)

func TestStreamWritersWriteXMLAndJSON(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "record.flv")
	metadata := Metadata{
		Platform:             "哔哩哔哩",
		RoomID:               "123",
		RoomTitle:            "测试直播",
		UserName:             "主播",
		RecordStartTimestamp: 1700000000000,
		CreatedAt:            time.UnixMilli(1700000000000),
	}
	writers, err := newStreamWriters(videoPath, []configs.DanmakuFormat{
		configs.DanmakuFormatXML,
		configs.DanmakuFormatJSON,
	}, metadata)
	if err != nil {
		t.Fatal(err)
	}
	message := Message{
		Type:      MessageTypeComment,
		Timestamp: 1700000005000,
		Text:      `hello <弹幕>`,
		Color:     "#ffffff",
		Mode:      1,
		Sender: Sender{
			UID:  "42",
			Name: `用户"一"`,
		},
	}
	for _, writer := range writers {
		if err := writer.AddMessage(message); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}

	xmlData, err := os.ReadFile(filepath.Join(dir, "record.xml"))
	if err != nil {
		t.Fatal(err)
	}
	xmlText := string(xmlData)
	if !strings.Contains(xmlText, "<i>") || !strings.Contains(xmlText, "</i>") {
		t.Fatalf("XML 缺少根节点: %s", xmlText)
	}
	if !strings.Contains(xmlText, "hello &lt;弹幕&gt;") {
		t.Fatalf("XML 未正确转义弹幕内容: %s", xmlText)
	}
	if !strings.Contains(xmlText, "user=\"用户&quot;一&quot;\"") {
		t.Fatalf("XML 未正确转义用户名: %s", xmlText)
	}

	jsonData, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Metadata Metadata  `json:"metadata"`
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("JSON 文件无效: %v\n%s", err, string(jsonData))
	}
	if decoded.Metadata.RoomID != "123" {
		t.Fatalf("metadata.room_id = %q", decoded.Metadata.RoomID)
	}
	if len(decoded.Messages) != 1 || decoded.Messages[0].Text != message.Text {
		t.Fatalf("messages = %+v", decoded.Messages)
	}
}
