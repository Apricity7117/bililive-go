package servers

import (
	"testing"

	"github.com/bililive-go/bililive-go/src/configs"
)

func TestApplyOverridableConfigUpdatesDanmakuPartialAndClear(t *testing.T) {
	oc := configs.OverridableConfig{}

	applyOverridableConfigUpdates(&oc, map[string]interface{}{
		"danmaku": map[string]interface{}{
			"enable": true,
		},
	})
	if oc.Danmaku == nil || oc.Danmaku.Enable == nil || !*oc.Danmaku.Enable {
		t.Fatalf("expected danmaku.enable override to be true, got %#v", oc.Danmaku)
	}
	if oc.Danmaku.SaveGift != nil || oc.Danmaku.UseServerTimestamp != nil || oc.Danmaku.UseCookie != nil {
		t.Fatalf("expected unspecified danmaku fields to keep inheriting, got %#v", oc.Danmaku)
	}

	applyOverridableConfigUpdates(&oc, map[string]interface{}{
		"danmaku": map[string]interface{}{
			"enable":     nil,
			"save_gift":  false,
			"use_cookie": false,
		},
	})
	if oc.Danmaku == nil ||
		oc.Danmaku.Enable != nil ||
		oc.Danmaku.SaveGift == nil || *oc.Danmaku.SaveGift ||
		oc.Danmaku.UseCookie == nil || *oc.Danmaku.UseCookie {
		t.Fatalf("expected enable cleared, save_gift false and use_cookie false override, got %#v", oc.Danmaku)
	}

	applyOverridableConfigUpdates(&oc, map[string]interface{}{
		"danmaku": map[string]interface{}{
			"save_gift":  nil,
			"use_cookie": nil,
		},
	})
	if oc.Danmaku != nil {
		t.Fatalf("expected empty danmaku override to be removed, got %#v", oc.Danmaku)
	}
}

func TestApplyOverridableConfigUpdatesDanmakuNullClearsAll(t *testing.T) {
	enabled := true
	oc := configs.OverridableConfig{
		Danmaku: &configs.DanmakuOverride{
			Enable: &enabled,
		},
	}

	applyOverridableConfigUpdates(&oc, map[string]interface{}{
		"danmaku": nil,
	})
	if oc.Danmaku != nil {
		t.Fatalf("expected danmaku override to be cleared, got %#v", oc.Danmaku)
	}
}
