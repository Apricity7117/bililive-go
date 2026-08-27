package servers

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	"github.com/bililive-go/bililive-go/src/configs"
)

func TestApplyOverridableDanmakuUpdatesPreservesInheritance(t *testing.T) {
	var override configs.OverridableConfig
	applyOverridableConfigUpdates(&override, map[string]interface{}{
		"danmaku": map[string]interface{}{
			"formats": []interface{}{"xml"},
		},
	})

	if assert.NotNil(t, override.Danmaku) {
		assert.Equal(t, []configs.DanmakuFormat{configs.DanmakuFormatXML}, override.Danmaku.Formats)
		assert.Zero(t, override.Danmaku.FontSize)
		assert.Nil(t, override.Danmaku.UseCookie)
	}
}

func TestGetSoopLiveAuthConfigDoesNotExposeSavedPassword(t *testing.T) {
	cfg := configs.NewConfig()
	cfg.SoopLiveAuth.Username = "tester"
	cfg.SoopLiveAuth.Password = "secret"
	configs.SetCurrentConfig(cfg)

	recorder := httptest.NewRecorder()
	getSoopLiveAuthConfig(recorder, nil)

	assert.Equal(t, 200, recorder.Code)

	var resp commonResp
	err := json.Unmarshal(recorder.Body.Bytes(), &resp)
	assert.NoError(t, err)

	data, ok := resp.Data.(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "tester", data["username"])
	assert.Equal(t, true, data["has_saved_credentials"])
	_, exists := data["password"]
	assert.False(t, exists)
}

func TestGetFileInfoAssociatesRootSidecarsWithPartVideo(t *testing.T) {
	tmpDir := t.TempDir()
	for _, name := range []string{"record_PART000.flv", "record.ass", "record.xml"} {
		assert.NoError(t, os.WriteFile(filepath.Join(tmpDir, name), []byte("data"), 0644))
	}
	cfg := configs.NewConfig()
	cfg.OutPutPath = tmpDir
	configs.SetCurrentConfig(cfg)

	req := httptest.NewRequest("GET", "/api/file-info/", nil)
	req = mux.SetURLVars(req, map[string]string{"path": ""})
	resp := httptest.NewRecorder()
	getFileInfo(resp, req)

	assert.Equal(t, 200, resp.Code)
	var payload struct {
		Files []struct {
			Name         string   `json:"name"`
			SubtitleFile string   `json:"subtitle_file"`
			DanmakuFiles []string `json:"danmaku_files"`
		} `json:"files"`
	}
	assert.NoError(t, json.Unmarshal(resp.Body.Bytes(), &payload))
	if assert.Len(t, payload.Files, 1) {
		assert.Equal(t, "record_PART000.flv", payload.Files[0].Name)
		assert.Equal(t, "record.ass", payload.Files[0].SubtitleFile)
		assert.ElementsMatch(t, []string{"record.ass", "record.xml"}, payload.Files[0].DanmakuFiles)
	}
}

func TestRemoveDanmakuSidecarsIncludesPartRoot(t *testing.T) {
	tmpDir := t.TempDir()
	partVideo := filepath.Join(tmpDir, "record_PART000.flv")
	for _, name := range []string{"record_PART000.ass", "record_PART000.xml", "record.ass", "record.xml"} {
		assert.NoError(t, os.WriteFile(filepath.Join(tmpDir, name), []byte("data"), 0644))
	}

	removeDanmakuSidecars(partVideo)
	for _, name := range []string{"record_PART000.ass", "record_PART000.xml", "record.ass", "record.xml"} {
		assert.NoFileExists(t, filepath.Join(tmpDir, name))
	}
}
