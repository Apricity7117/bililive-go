package danmaku

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/bililive-go/bililive-go/src/configs"
)

// DanmakuBroadcastCallback 弹幕实时广播回调函数类型。
// 当设置时，每条弹幕/礼物/SC/舰长消息都会同时通过此回调广播。
type DanmakuBroadcastCallback func(msgType, username, content string, extra map[string]interface{})

// baseRecorder 提供三个平台弹幕录制器的公共字段和方法。
type baseRecorder struct {
	mu           sync.Mutex
	running      bool
	count        int
	writers      []eventWriter
	outputFile   string
	outputFiles  []string
	writerErrors map[string]string
	cfg          configs.DanmakuConfig
	logger       *logrus.Entry
	startAt      time.Time
	broadcastCb  DanmakuBroadcastCallback
	eventsWg     sync.WaitGroup
	metadata     XMLMetadata
}

// initWriters 按格式初始化 ASS/XML 输出器；单个格式失败不影响其他格式。
func (b *baseRecorder) initWriters(startAt time.Time, platform, roomID, title string) error {
	formats := b.cfg.NormalizeFormats()
	if len(formats) == 0 {
		return fmt.Errorf("没有有效的弹幕输出格式")
	}

	b.startAt = startAt
	b.writerErrors = make(map[string]string)
	b.writers = nil
	b.outputFiles = nil
	for _, format := range formats {
		path := replaceOutputExtension(b.outputFile, string(format))
		var (
			writer eventWriter
			err    error
		)
		switch format {
		case configs.DanmakuFormatASS:
			var ass *AssWriter
			ass, err = NewAssWriter(path, startAt, b.cfg, title)
			if err == nil {
				writer = &assEventWriter{writer: ass}
			}
		case configs.DanmakuFormatXML:
			metadata := b.metadata
			metadata.Platform = platform
			metadata.RoomID = roomID
			metadata.VideoStartTime = startAt.UnixMilli()
			writer, err = NewXMLWriter(path, startAt, metadata, b.cfg.ServerTimestampEnabled())
		default:
			continue
		}
		if err != nil {
			b.writerErrors[string(format)] = err.Error()
			_ = os.Remove(path)
			continue
		}
		b.writers = append(b.writers, writer)
		b.outputFiles = append(b.outputFiles, writer.Path())
	}
	if len(b.writers) == 0 {
		return fmt.Errorf("所有弹幕输出器初始化失败")
	}
	return nil
}

// SetXMLMetadata 设置 XML 头部需要的房间标题、主播名和开播时间。
// 未设置的字段会由 initWriters 使用平台和房间 ID 的基础元数据补齐。
func (b *baseRecorder) SetXMLMetadata(metadata XMLMetadata) {
	b.mu.Lock()
	b.metadata = metadata
	b.mu.Unlock()
}

func replaceOutputExtension(path, extension string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + "." + extension
}

// OutputFile 返回兼容旧接口的单个输出路径，ASS 存在时优先返回 ASS。
func (b *baseRecorder) OutputFile() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, path := range b.outputFiles {
		if strings.EqualFold(filepath.Ext(path), ".ass") {
			return path
		}
	}
	if len(b.outputFiles) > 0 {
		return b.outputFiles[0]
	}
	return b.outputFile
}

// OutputFiles 返回当前录制器创建的全部弹幕文件。
func (b *baseRecorder) OutputFiles() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.outputFiles...)
}

// GetCount 返回已接收的逻辑事件数。
func (b *baseRecorder) GetCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// IsRunning 返回弹幕录制是否运行中。
func (b *baseRecorder) IsRunning() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}

// GetStatus 返回弹幕录制状态及全部输出文件。
func (b *baseRecorder) GetStatus() map[string]interface{} {
	b.mu.Lock()
	defer b.mu.Unlock()

	output := ""
	for _, path := range b.outputFiles {
		if strings.EqualFold(filepath.Ext(path), ".ass") {
			output = path
			break
		}
	}
	if output == "" && len(b.outputFiles) > 0 {
		output = b.outputFiles[0]
	}
	status := map[string]interface{}{
		"danmaku_running": b.running,
		"danmaku_count":   b.count,
		"danmaku_output":  output,
		"danmaku_outputs": append([]string(nil), b.outputFiles...),
	}
	if len(b.writerErrors) > 0 {
		errors := make(map[string]string, len(b.writerErrors))
		for format, errText := range b.writerErrors {
			errors[format] = errText
		}
		status["danmaku_writer_errors"] = errors
	}
	if b.running {
		status["danmaku_start_time"] = b.startAt.Format(time.RFC3339)
	}
	return status
}

// stopBase 通用停止逻辑：标记停止、清空 writer，返回旧引用供调用方关闭。
func (b *baseRecorder) stopBase() []eventWriter {
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return nil
	}
	b.running = false
	writers := b.writers
	b.writers = nil
	b.mu.Unlock()
	// Stop 连接后等待已经取到 writer 快照的回调完成，避免 Close 与 WriteEvent 并发。
	b.eventsWg.Wait()
	return writers
}

func (b *baseRecorder) closeWriters(writers []eventWriter) {
	for _, writer := range writers {
		if err := writer.Close(); err != nil && b.logger != nil {
			b.mu.Lock()
			if b.writerErrors == nil {
				b.writerErrors = make(map[string]string)
			}
			if _, exists := b.writerErrors[string(writer.Format())]; !exists {
				b.writerErrors[string(writer.Format())] = err.Error()
			}
			b.mu.Unlock()
			b.logger.WithError(err).Warnf("关闭弹幕文件失败: %s", writer.Path())
		}
	}
}

// discardWritersLocked 在已持有状态锁时回滚本次启动创建的侧车文件。
func (b *baseRecorder) discardWritersLocked() {
	writers := b.writers
	paths := append([]string(nil), b.outputFiles...)
	b.writers = nil
	b.outputFiles = nil
	for _, writer := range writers {
		_ = writer.Close()
	}
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

// SetBroadcastCallback 设置弹幕广播回调（用于 SSE 实时推送）。
func (b *baseRecorder) SetBroadcastCallback(cb DanmakuBroadcastCallback) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.broadcastCb = cb
}

func (b *baseRecorder) addEvent(event Event, broadcastType string, extra map[string]interface{}) {
	b.mu.Lock()
	if !b.running || len(b.writers) == 0 {
		b.mu.Unlock()
		return
	}
	b.eventsWg.Add(1)
	defer b.eventsWg.Done()
	event = NormalizeEvent(event)
	writers := append([]eventWriter(nil), b.writers...)
	b.count++
	callback := b.broadcastCb
	b.mu.Unlock()

	for _, writer := range writers {
		format := string(writer.Format())
		b.mu.Lock()
		failed := b.writerErrors != nil && b.writerErrors[format] != ""
		b.mu.Unlock()
		if failed {
			continue
		}
		if err := writer.WriteEvent(event); err != nil {
			b.mu.Lock()
			if b.writerErrors == nil {
				b.writerErrors = make(map[string]string)
			}
			if _, exists := b.writerErrors[format]; !exists {
				b.writerErrors[format] = err.Error()
				if b.logger != nil {
					b.logger.WithError(err).Warnf("写入弹幕文件失败: %s", writer.Path())
				}
			}
			b.mu.Unlock()
		}
	}
	if callback != nil {
		callback(broadcastType, event.Username, event.TextOrName(), extra)
	}
}

// TextOrName 返回适合 SSE 展示的事件文本。
func (e Event) TextOrName() string {
	if e.Text != "" {
		return e.Text
	}
	return e.Name
}
