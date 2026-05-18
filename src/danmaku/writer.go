package danmaku

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/bililive-go/bililive-go/src/configs"
)

type streamWriter interface {
	AddMessage(Message) error
	Close() error
	Path() string
}

func newStreamWriters(videoPath string, formats []configs.DanmakuFormat, metadata Metadata) ([]streamWriter, error) {
	writers := make([]streamWriter, 0, len(formats))
	for _, format := range formats {
		var (
			writer streamWriter
			err    error
		)
		switch format {
		case configs.DanmakuFormatXML:
			writer, err = newXMLWriter(replaceExt(videoPath, ".xml"), metadata)
		case configs.DanmakuFormatJSON:
			writer, err = newJSONWriter(replaceExt(videoPath, ".json"), metadata)
		default:
			continue
		}
		if err != nil {
			for _, w := range writers {
				_ = w.Close()
			}
			return nil, err
		}
		writers = append(writers, writer)
	}
	return writers, nil
}

func replaceExt(path, ext string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ext
}

type xmlWriter struct {
	mu        sync.Mutex
	file      *os.File
	writer    *bufio.Writer
	path      string
	metadata  Metadata
	closed    bool
	startTime int64
}

func newXMLWriter(path string, metadata Metadata) (*xmlWriter, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &xmlWriter{
		file:      file,
		writer:    bufio.NewWriter(file),
		path:      path,
		metadata:  metadata,
		startTime: metadata.RecordStartTimestamp,
	}
	if err := w.writeHeader(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return w, nil
}

func (w *xmlWriter) Path() string {
	return w.path
}

func (w *xmlWriter) AddMessage(message Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	switch message.Type {
	case MessageTypeComment:
		if err := w.writeComment(message); err != nil {
			return err
		}
	case MessageTypeGift:
		if err := w.writeGift(message); err != nil {
			return err
		}
	case MessageTypeSuperChat:
		if err := w.writeSuperChat(message); err != nil {
			return err
		}
	case MessageTypeGuard:
		if err := w.writeGuard(message); err != nil {
			return err
		}
	default:
		return nil
	}
	return w.writer.Flush()
}

func (w *xmlWriter) Close() error {
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

func (w *xmlWriter) writeHeader() error {
	_, err := fmt.Fprintf(w.writer, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<i>\n%s", metadataXML(w.metadata))
	if err != nil {
		return err
	}
	return w.writer.Flush()
}

func (w *xmlWriter) writeComment(message Message) error {
	progress := progressSeconds(w.startTime, message.Timestamp)
	color := parseColor(message.Color)
	uid := message.Sender.UID
	mode := message.Mode
	if mode == 0 {
		mode = 1
	}
	p := strings.Join([]string{
		formatSeconds(progress),
		strconv.Itoa(mode),
		"25",
		strconv.FormatInt(color, 10),
		strconv.FormatInt(normalizeTimestamp(message.Timestamp), 10),
		"0",
		uid,
		uid,
		"0",
	}, ",")
	_, err := fmt.Fprintf(
		w.writer,
		"<d p=\"%s\" user=\"%s\" uid=\"%s\" timestamp=\"%d\">%s</d>\n",
		escapeXML(p),
		escapeXML(message.Sender.Name),
		escapeXML(uid),
		normalizeTimestamp(message.Timestamp),
		escapeXML(message.Text),
	)
	return err
}

func (w *xmlWriter) writeGift(message Message) error {
	_, err := fmt.Fprintf(
		w.writer,
		"<gift ts=\"%s\" giftname=\"%s\" giftcount=\"%d\" price=\"%s\" user=\"%s\" uid=\"%s\" timestamp=\"%d\"></gift>\n",
		formatSeconds(progressSeconds(w.startTime, message.Timestamp)),
		escapeXML(message.Name),
		message.Count,
		priceMilli(message.Price),
		escapeXML(message.Sender.Name),
		escapeXML(message.Sender.UID),
		normalizeTimestamp(message.Timestamp),
	)
	return err
}

func (w *xmlWriter) writeSuperChat(message Message) error {
	_, err := fmt.Fprintf(
		w.writer,
		"<sc ts=\"%s\" price=\"%s\" time=\"%d\" user=\"%s\" uid=\"%s\" timestamp=\"%d\">%s</sc>\n",
		formatSeconds(progressSeconds(w.startTime, message.Timestamp)),
		priceMilli(message.Price),
		message.Duration,
		escapeXML(message.Sender.Name),
		escapeXML(message.Sender.UID),
		normalizeTimestamp(message.Timestamp),
		escapeXML(message.Text),
	)
	return err
}

func (w *xmlWriter) writeGuard(message Message) error {
	count := message.Count
	if count == 0 {
		count = 1
	}
	_, err := fmt.Fprintf(
		w.writer,
		"<guard ts=\"%s\" giftname=\"%s\" giftcount=\"%d\" price=\"%s\" level=\"%d\" user=\"%s\" uid=\"%s\" timestamp=\"%d\"></guard>\n",
		formatSeconds(progressSeconds(w.startTime, message.Timestamp)),
		escapeXML(message.Name),
		count,
		priceMilli(message.Price),
		message.Level,
		escapeXML(message.Sender.Name),
		escapeXML(message.Sender.UID),
		normalizeTimestamp(message.Timestamp),
	)
	return err
}

type jsonWriter struct {
	mu      sync.Mutex
	file    *os.File
	writer  *bufio.Writer
	path    string
	closed  bool
	written int
}

func newJSONWriter(path string, metadata Metadata) (*jsonWriter, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &jsonWriter{
		file:   file,
		writer: bufio.NewWriter(file),
		path:   path,
	}
	header, err := json.Marshal(struct {
		Metadata Metadata `json:"metadata"`
	}{
		Metadata: metadata,
	})
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(w.writer, "%s", strings.TrimSuffix(string(header), "}")+`, "messages": [`); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := w.writer.Flush(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return w, nil
}

func (w *jsonWriter) Path() string {
	return w.path
}

func (w *jsonWriter) AddMessage(message Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if w.written > 0 {
		if _, err := w.writer.WriteString(","); err != nil {
			return err
		}
	}
	if _, err := w.writer.WriteString("\n"); err != nil {
		return err
	}
	if _, err := w.writer.Write(data); err != nil {
		return err
	}
	w.written++
	return w.writer.Flush()
}

func (w *jsonWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if _, err := w.writer.WriteString("\n]}\n"); err != nil {
		_ = w.file.Close()
		return err
	}
	if err := w.writer.Flush(); err != nil {
		_ = w.file.Close()
		return err
	}
	return w.file.Close()
}

func metadataXML(metadata Metadata) string {
	var b strings.Builder
	b.WriteString("<metadata>\n")
	writeXMLElement(&b, "platform", metadata.Platform)
	writeXMLElement(&b, "room_id", metadata.RoomID)
	writeXMLElement(&b, "room_title", metadata.RoomTitle)
	writeXMLElement(&b, "user_name", metadata.UserName)
	writeXMLElement(&b, "video_start_time", strconv.FormatInt(metadata.RecordStartTimestamp, 10))
	if metadata.LiveStartTimestamp > 0 {
		writeXMLElement(&b, "live_start_time", strconv.FormatInt(metadata.LiveStartTimestamp, 10))
	}
	b.WriteString("</metadata>\n")
	return b.String()
}

func writeXMLElement(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "<%s>%s</%s>\n", name, escapeXML(value), name)
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

func progressSeconds(startTimestamp, timestamp int64) float64 {
	progress := float64(normalizeTimestamp(timestamp)-normalizeTimestamp(startTimestamp)) / 1000
	if progress < 0 {
		return 0
	}
	return progress
}

func normalizeTimestamp(timestamp int64) int64 {
	if timestamp > 0 && timestamp < 1_000_000_000_000 {
		return timestamp * 1000
	}
	return timestamp
}

func formatSeconds(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func parseColor(color string) int64 {
	color = strings.TrimPrefix(strings.TrimSpace(color), "#")
	if color == "" {
		return 16777215
	}
	parsed, err := strconv.ParseInt(color, 16, 64)
	if err != nil {
		return 16777215
	}
	return parsed
}

func priceMilli(price float64) string {
	return strconv.FormatInt(int64(math.Round(price*1000)), 10)
}
