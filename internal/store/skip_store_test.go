package store

import "testing"

// TestSkipSettingLifecycle 验证跳过设置：空库、UPSERT 覆盖、开关翻转、负值钳制、删除幂等。
func TestSkipSettingLifecycle(t *testing.T) {
	st := openTestStore(t)

	// 空库查询返回 nil
	got, err := st.GetSkipSetting("vid-1")
	if err != nil || got != nil {
		t.Fatalf("空库查询应返回 nil, got=%v err=%v", got, err)
	}

	// 首次写入
	if err := st.UpsertSkipSetting("vid-1", SkipSetting{
		IntroSeconds: 90, OutroSeconds: 60, Enabled: true,
	}); err != nil {
		t.Fatalf("首次 Upsert 失败: %v", err)
	}
	got, err = st.GetSkipSetting("vid-1")
	if err != nil || got == nil {
		t.Fatalf("回读失败: got=%v err=%v", got, err)
	}
	if got.IntroSeconds != 90 || got.OutroSeconds != 60 || !got.Enabled {
		t.Fatalf("回读字段不符: %+v", got)
	}

	// 二次写入为覆盖而非新增行
	if err := st.UpsertSkipSetting("vid-1", SkipSetting{
		IntroSeconds: 120, OutroSeconds: 30, Enabled: false,
	}); err != nil {
		t.Fatalf("二次 Upsert 失败: %v", err)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(1) FROM skip_settings WHERE video_id = ?`, "vid-1").Scan(&n); err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("UPSERT 后应仍为 1 行，实际 %d", n)
	}
	got, _ = st.GetSkipSetting("vid-1")
	if got.IntroSeconds != 120 || got.OutroSeconds != 30 || got.Enabled {
		t.Fatalf("覆盖后字段不符: %+v", got)
	}

	// 开关重新打开
	if err := st.UpsertSkipSetting("vid-1", SkipSetting{
		IntroSeconds: 120, OutroSeconds: 30, Enabled: true,
	}); err != nil {
		t.Fatalf("启用 Upsert 失败: %v", err)
	}
	got, _ = st.GetSkipSetting("vid-1")
	if !got.Enabled {
		t.Fatalf("期望 enabled=true，实际 false")
	}

	// 负值钳制为 0
	if err := st.UpsertSkipSetting("vid-2", SkipSetting{
		IntroSeconds: -5, OutroSeconds: -9, Enabled: true,
	}); err != nil {
		t.Fatalf("负值 Upsert 失败: %v", err)
	}
	got, _ = st.GetSkipSetting("vid-2")
	if got.IntroSeconds != 0 || got.OutroSeconds != 0 {
		t.Fatalf("负值应钳制为 0，实际 %+v", got)
	}

	// 空 videoID 报错
	if err := st.UpsertSkipSetting("", SkipSetting{Enabled: true}); err == nil {
		t.Fatal("空 videoID 应返回错误")
	}

	// 删除：存在记录后删除，再查为 nil；删除不存在的 ID 不报错
	if err := st.DeleteSkipSetting("vid-1"); err != nil {
		t.Fatalf("DeleteSkipSetting 失败: %v", err)
	}
	if got, err := st.GetSkipSetting("vid-1"); err != nil || got != nil {
		t.Fatalf("删除后应返回 nil, got=%v err=%v", got, err)
	}
	if err := st.DeleteSkipSetting("not-exist"); err != nil {
		t.Fatalf("删除不存在的记录应幂等，实际报错: %v", err)
	}
}

// TestSkipSettingPrune 验证重扫清理：孤儿设置被删、在库视频保留、空合集全清。
func TestSkipSettingPrune(t *testing.T) {
	st := openTestStore(t)

	if err := st.UpsertSkipSetting("real", SkipSetting{IntroSeconds: 10, Enabled: true}); err != nil {
		t.Fatalf("写入 real 失败: %v", err)
	}
	if err := st.UpsertSkipSetting("ghost", SkipSetting{IntroSeconds: 20, Enabled: true}); err != nil {
		t.Fatalf("写入 ghost 失败: %v", err)
	}

	// 非空合集：ghost 被清，real 保留
	if err := st.Prune(map[string]bool{"real": true}); err != nil {
		t.Fatalf("Prune 失败: %v", err)
	}
	if got, _ := st.GetSkipSetting("real"); got == nil {
		t.Error("real 的设置应保留")
	}
	if got, _ := st.GetSkipSetting("ghost"); got != nil {
		t.Error("ghost 的孤儿设置应被清理")
	}

	// 空合集：全表清空
	if err := st.Prune(map[string]bool{}); err != nil {
		t.Fatalf("空合集 Prune 失败: %v", err)
	}
	if got, _ := st.GetSkipSetting("real"); got != nil {
		t.Error("空合集清理后应无残留设置")
	}
}
