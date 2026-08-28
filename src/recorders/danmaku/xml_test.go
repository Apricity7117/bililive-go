package danmaku

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
)

func TestXMLWriterGoldenOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.xml")
	start := time.UnixMilli(1700000000000)
	writer, err := NewXMLWriter(path, start, XMLMetadata{
		Platform:       "哔哩哔哩",
		RoomID:         "123",
		RoomTitle:      "直播标题",
		UserName:       "主播名称",
		VideoStartTime: 1700000000000,
		LiveStartTime:  1699999900000,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{Type: EventComment, ReceivedAt: start.Add(5 * time.Second), EventTimestamp: 1700000005000, Text: "弹幕内容", Username: "用户", UID: "42"},
		{Type: EventGift, ReceivedAt: start.Add(8 * time.Second), EventTimestamp: 1700000008000, Name: "辣条", Count: 3, PriceMilli: 1000, Username: "用户", UID: "42"},
		{Type: EventSuperChat, ReceivedAt: start.Add(12 * time.Second), EventTimestamp: 1700000012000, Text: "醒目留言", Duration: 60, PriceMilli: 30000, Username: "用户", UID: "42"},
		{Type: EventGuard, ReceivedAt: start.Add(15 * time.Second), EventTimestamp: 1700000015000, Name: "舰长", Count: 1, PriceMilli: 198000, Level: 3, Username: "用户", UID: "42"},
	}
	for _, event := range events {
		if err := writer.WriteEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	want := `<?xml version="1.0" encoding="utf-8"?>
<i>
<metadata>
<platform>哔哩哔哩</platform>
<room_id>123</room_id>
<room_title>直播标题</room_title>
<user_name>主播名称</user_name>
<video_start_time>1700000000000</video_start_time>
<live_start_time>1699999900000</live_start_time>
</metadata>
<d p="5.000,1,25,16777215,1700000005000,0,42,42,0" user="用户" uid="42" timestamp="1700000005000">弹幕内容</d>
<gift ts="8.000" giftname="辣条" giftcount="3" price="1000" user="用户" uid="42" timestamp="1700000008000"></gift>
<sc ts="12.000" price="30000" time="60" user="用户" uid="42" timestamp="1700000012000">醒目留言</sc>
<guard ts="15.000" giftname="舰长" giftcount="1" price="198000" level="3" user="用户" uid="42" timestamp="1700000015000"></guard>
</i>
`
	// Raw string 中的换行即为文件中的换行。
	if got != strings.ReplaceAll(want, `\n`, "\n") {
		t.Fatalf("XML golden 不匹配:\n%s", got)
	}
	var document struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(data, &document); err != nil {
		t.Fatalf("XML 无法被标准解析器解析: %v", err)
	}
}

func TestASSEventWriterPreservesPriceUnits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.ass")
	start := time.UnixMilli(1700000000000)
	cfg := configs.GetDefaultDanmakuConfig()
	ass, err := NewAssWriter(path, start, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	writer := &assEventWriter{writer: ass}
	for _, event := range []Event{
		{Type: EventGift, ReceivedAt: start, Name: "礼物", Count: 2, PriceMilli: 1500, CoinType: "gold", Username: "用户"},
		{Type: EventGuard, ReceivedAt: start, Name: "舰长", PriceMilli: 198000, Username: "用户"},
		{Type: EventSuperChat, ReceivedAt: start, Text: "留言", PriceMilli: 30000, Username: "用户"},
	} {
		if err := writer.WriteEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"[礼物 ¥3.0]", "[舰长 ¥198]", "[SC ¥30]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ASS 缺少价格展示 %q:\n%s", want, got)
		}
	}
}

func TestXMLWriterEscapesAndClampsValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escaped.xml")
	start := time.UnixMilli(1700000000000)
	writer, err := NewXMLWriter(path, start, XMLMetadata{VideoStartTime: start.UnixMilli()}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteEvent(Event{
		Type:           EventComment,
		ReceivedAt:     start,
		EventTimestamp: 1699999999000,
		Text:           "a&<b>\nnext",
		Username:       `u"'`,
		UID:            `x&`,
		Color:          0,
		Mode:           0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteEvent(Event{
		Type:       EventGift,
		ReceivedAt: start,
		Count:      0,
		PriceMilli: -1,
		Name:       "礼物",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, expected := range []string{
		`<d p="0.000,1,25,16777215,1699999999000,0,x&amp;,x&amp;,0" user="u&quot;&#39;" uid="x&amp;" timestamp="1699999999000">a&amp;&lt;b&gt;next</d>`,
		`<gift ts="0.000" giftname="礼物" giftcount="1" price="0" user="" uid="" timestamp="1700000000000"></gift>`,
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("XML 缺少预期内容 %q:\n%s", expected, got)
		}
	}
}
