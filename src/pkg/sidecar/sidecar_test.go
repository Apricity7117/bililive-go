package sidecar

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSidecarPartAssociation(t *testing.T) {
	if !AssociatedBase("record_PART000", "record") {
		t.Fatal("PART 视频应关联根侧车")
	}
	if AssociatedBase("record_PART00A", "record") {
		t.Fatal("非数字 PART 后缀不应匹配")
	}
	paths := SidecarPaths(filepath.Join("/tmp", "record_PART000.flv"))
	if len(paths) != 4 || paths[2] != filepath.Join("/tmp", "record.ass") {
		t.Fatalf("PART 侧车路径错误: %#v", paths)
	}
}

func TestHasVideoForBaseIncludesPart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "record_PART000.flv"), []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	if !HasVideoForBase(dir, "record") {
		t.Fatal("应识别 PART 视频")
	}
}

