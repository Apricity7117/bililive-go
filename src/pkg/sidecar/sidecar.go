// Package sidecar 提供视频与 ASS/XML 弹幕侧车文件的关联规则。
package sidecar

import (
	"os"
	"path/filepath"
	"strings"
)

var videoExtensions = []string{".flv", ".mkv", ".ts", ".mp4"}

// IsVideo 判断路径是否为录制视频文件。
func IsVideo(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flv", ".mkv", ".ts", ".mp4":
		return true
	default:
		return false
	}
}

// IsDanmaku 判断路径是否为 ASS/XML 弹幕侧车。
func IsDanmaku(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ass", ".xml":
		return true
	default:
		return false
	}
}

// Base 返回去除扩展名后的文件基名，保留原始大小写。
func Base(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// IsPartBase 判断 videoBase 是否为 rootBase 的录播姬三位序号分段。
func IsPartBase(videoBase, rootBase string) bool {
	if !strings.HasPrefix(videoBase, rootBase+"_PART") {
		return false
	}
	suffix := strings.TrimPrefix(videoBase, rootBase+"_PART")
	if len(suffix) != 3 {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// AssociatedBase 判断视频基名与侧车基名是否匹配（包括录播姬 PART 分段）。
func AssociatedBase(videoBase, sidecarBase string) bool {
	return videoBase == sidecarBase || IsPartBase(videoBase, sidecarBase)
}

// SidecarPaths 返回视频对应的 ASS/XML 路径；PART 视频同时包含根视频基名侧车，
// 用于上传和删除时避免把同一份弹幕复制成多个 PART 文件。
func SidecarPaths(videoPath string) []string {
	dir := filepath.Dir(videoPath)
	base := Base(videoPath)
	bases := []string{base}
	if idx := strings.LastIndex(base, "_PART"); idx > 0 {
		root := base[:idx]
		if IsPartBase(base, root) {
			bases = append(bases, root)
		}
	}
	paths := make([]string, 0, len(bases)*2)
	for _, candidate := range bases {
		paths = append(paths, filepath.Join(dir, candidate+".ass"), filepath.Join(dir, candidate+".xml"))
	}
	return paths
}

// HasVideoForBase 判断基名对应的视频或 PART 分段是否存在。
func HasVideoForBase(dir, base string) bool {
	for _, ext := range videoExtensions {
		if _, err := os.Stat(filepath.Join(dir, base+ext)); err == nil {
			return true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !IsVideo(name) {
			continue
		}
		if IsPartBase(Base(name), base) {
			return true
		}
	}
	return false
}

