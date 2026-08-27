package danmaku

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
)

// XMLMetadata 描述一个录制片段的 XML 元数据。
type XMLMetadata struct {
	Platform       string
	RoomID         string
	RoomTitle      string
	UserName       string
	VideoStartTime int64
	LiveStartTime  int64
}

// XMLWriter 将统一事件写为旧版兼容 XML。
type XMLWriter struct {
	mu                 sync.Mutex
	file               *os.File
	writer             *bufio.Writer
	path               string
	startAt            time.Time
	startTimestamp     int64
	useServerTimestamp bool
	closed             bool
}

// NewXMLWriter 创建 XML 输出文件并写入声明、根节点和元数据。
func NewXMLWriter(path string, startAt time.Time, metadata XMLMetadata, useServerTimestamp bool) (*XMLWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}
	w := &XMLWriter{
		file:               file,
		writer:             bufio.NewWriter(file),
		path:               path,
		startAt:            startAt,
		startTimestamp:     normalizeTimestamp(metadata.VideoStartTime),
		useServerTimestamp: useServerTimestamp,
	}
	if w.startTimestamp <= 0 {
		w.startTimestamp = startAt.UnixMilli()
	}
	if err := w.writeHeader(metadata); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return w, nil
}

// Format 返回输出格式。
func (w *XMLWriter) Format() configs.DanmakuFormat { return configs.DanmakuFormatXML }

// Path 返回输出文件路径。
func (w *XMLWriter) Path() string { return w.path }

// WriteEvent 写入单条事件并立即刷新缓冲区。
func (w *XMLWriter) WriteEvent(event Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	event = NormalizeEvent(event)
	var err error
	switch event.Type {
	case EventComment:
		err = w.writeComment(event)
	case EventGift:
		err = w.writeGift(event)
	case EventSuperChat:
		err = w.writeSuperChat(event)
	case EventGuard:
		err = w.writeGuard(event)
	}
	if err != nil {
		return err
	}
	return w.writer.Flush()
}

// Close 完成根节点并关闭文件；重复调用是安全的。
func (w *XMLWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if _, err := w.writer.WriteString("</i>\n"); err != nil {
		_ = w.file.Close()
		return err
	}
	if err := w.writer.Flush(); err != nil {
		_ = w.file.Close()
		return err
	}
	return w.file.Close()
}

func (w *XMLWriter) writeHeader(metadata XMLMetadata) error {
	if _, err := fmt.Fprint(w.writer, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<i>\n<metadata>\n"); err != nil {
		return err
	}
	for _, element := range []struct {
		name  string
		value string
	}{
		{name: "platform", value: metadata.Platform},
		{name: "room_id", value: metadata.RoomID},
		{name: "room_title", value: metadata.RoomTitle},
		{name: "user_name", value: metadata.UserName},
		{name: "video_start_time", value: strconv.FormatInt(w.startTimestamp, 10)},
	} {
		if err := writeXMLElement(w.writer, element.name, element.value); err != nil {
			return err
		}
	}
	if metadata.LiveStartTime > 0 {
		if err := writeXMLElement(w.writer, "live_start_time", strconv.FormatInt(normalizeTimestamp(metadata.LiveStartTime), 10)); err != nil {
			return err
		}
	}
	if _, err := w.writer.WriteString("</metadata>\n"); err != nil {
		return err
	}
	return w.writer.Flush()
}

func (w *XMLWriter) selectedTimestamp(event Event) int64 {
	if w.useServerTimestamp {
		return normalizeTimestamp(event.EventTimestamp)
	}
	return event.ReceivedAt.UnixMilli()
}

func (w *XMLWriter) relativeSeconds(event Event) string {
	delta := w.selectedTimestamp(event) - w.startTimestamp
	if delta < 0 {
		delta = 0
	}
	return fmt.Sprintf("%.3f", float64(delta)/1000)
}

func (w *XMLWriter) writeComment(event Event) error {
	timestamp := w.selectedTimestamp(event)
	uid := event.UID
	p := strings.Join([]string{
		w.relativeSeconds(event),
		strconv.Itoa(event.Mode),
		"25",
		strconv.Itoa(event.Color),
		strconv.FormatInt(timestamp, 10),
		"0", uid, uid, "0",
	}, ",")
	_, err := fmt.Fprintf(w.writer, "<d p=\"%s\" user=\"%s\" uid=\"%s\" timestamp=\"%d\">%s</d>\n",
		escapeXML(p), escapeXML(event.Username), escapeXML(uid), timestamp, escapeXML(event.Text))
	return err
}

func (w *XMLWriter) writeGift(event Event) error {
	_, err := fmt.Fprintf(w.writer, "<gift ts=\"%s\" giftname=\"%s\" giftcount=\"%d\" price=\"%d\" user=\"%s\" uid=\"%s\" timestamp=\"%d\"></gift>\n",
		w.relativeSeconds(event), escapeXML(event.Name), event.Count, event.PriceMilli, escapeXML(event.Username), escapeXML(event.UID), w.selectedTimestamp(event))
	return err
}

func (w *XMLWriter) writeSuperChat(event Event) error {
	_, err := fmt.Fprintf(w.writer, "<sc ts=\"%s\" price=\"%d\" time=\"%d\" user=\"%s\" uid=\"%s\" timestamp=\"%d\">%s</sc>\n",
		w.relativeSeconds(event), event.PriceMilli, event.Duration, escapeXML(event.Username), escapeXML(event.UID), w.selectedTimestamp(event), escapeXML(event.Text))
	return err
}

func (w *XMLWriter) writeGuard(event Event) error {
	_, err := fmt.Fprintf(w.writer, "<guard ts=\"%s\" giftname=\"%s\" giftcount=\"%d\" price=\"%d\" level=\"%d\" user=\"%s\" uid=\"%s\" timestamp=\"%d\"></guard>\n",
		w.relativeSeconds(event), escapeXML(event.Name), event.Count, event.PriceMilli, event.Level, escapeXML(event.Username), escapeXML(event.UID), w.selectedTimestamp(event))
	return err
}

func writeXMLElement(writer *bufio.Writer, name, value string) error {
	if value == "" {
		return nil
	}
	_, err := fmt.Fprintf(writer, "<%s>%s</%s>\n", name, escapeXML(value), name)
	return err
}

func escapeXML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}
