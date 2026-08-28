package stages

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/stretchr/testify/require"
)

func TestBurnSubtitlesSkipsWhenOnlyXMLSidecarExists(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "record.flv")
	xmlPath := filepath.Join(dir, "record.xml")
	require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0644))
	require.NoError(t, os.WriteFile(xmlPath, []byte("<i></i>"), 0644))

	stage, err := NewBurnSubtitlesStage(pipeline.StageConfig{})
	require.NoError(t, err)
	ctx := &pipeline.PipelineContext{Ctx: context.Background()}
	input := []pipeline.FileInfo{
		{Path: videoPath, Type: pipeline.FileTypeVideo},
		{Path: xmlPath, Type: pipeline.FileTypeOther},
	}

	output, err := stage.Execute(ctx, input)
	require.NoError(t, err)
	require.Equal(t, input, output)
	require.Contains(t, stage.(*BurnSubtitlesStage).GetLogs(), "跳过烧录")
}
