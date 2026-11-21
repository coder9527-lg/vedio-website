package store

import (
	"database/sql"
	"fmt"
	"time"
)

// SkipSetting 是单个视频的片头片尾自动跳过设置。
type SkipSetting struct {
	VideoID      string    `json:"videoId"`
	IntroSeconds float64   `json:"introSeconds"` // 片头长度（从 0 起算的秒数），0 表示不跳过片头
	OutroSeconds float64   `json:"outroSeconds"` // 片尾长度（距视频结尾的秒数），0 表示不跳过片尾
	Enabled      bool      `json:"enabled"`      // 停用只关开关、不删数据
	UpdatedAt    time.Time `json:"updatedAt"`
}

// skipColumns 集中维护 skip_settings 的完整列，避免多处手写漂移。
const skipColumns = `video_id, intro_seconds, outro_seconds, enabled, updated_at`

// UpsertSkipSetting 覆盖式写入一个视频的跳过设置；同 video_id 再次写入即更新。
// 负值在 store 层兜底归零，与 SaveProgress 的钳制风格保持一致。
func (s *Store) UpsertSkipSetting(videoID string, in SkipSetting) error {
	if videoID == "" {
		return fmt.Errorf("videoID 不能为空")
	}
	if in.IntroSeconds < 0 {
		in.IntroSeconds = 0
	}
	if in.OutroSeconds < 0 {
		in.OutroSeconds = 0
	}
	enabled := 0
	if in.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO skip_settings(video_id, intro_seconds, outro_seconds, enabled, updated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(video_id) DO UPDATE SET
		   intro_seconds=excluded.intro_seconds,
		   outro_seconds=excluded.outro_seconds,
		   enabled=excluded.enabled,
		   updated_at=excluded.updated_at`,
		videoID, in.IntroSeconds, in.OutroSeconds, enabled, time.Now().UTC(),
	)
	return err
}

// scanSkipSetting 统一收口一行 skip_settings 的扫描，列顺序须与 skipColumns 一致。
func scanSkipSetting(scanner interface{ Scan(...any) error }) (SkipSetting, error) {
	var out SkipSetting
	var enabled int
	if err := scanner.Scan(&out.VideoID, &out.IntroSeconds, &out.OutroSeconds,
		&enabled, &out.UpdatedAt); err != nil {
		return SkipSetting{}, err
	}
	out.Enabled = enabled != 0
	return out, nil
}

// GetSkipSetting 查询单个视频的跳过设置，无记录时返回 (nil, nil)，与 GetProgress 同契约。
func (s *Store) GetSkipSetting(videoID string) (*SkipSetting, error) {
	row := s.db.QueryRow(
		`SELECT `+skipColumns+` FROM skip_settings WHERE video_id = ?`,
		videoID,
	)
	out, err := scanSkipSetting(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// DeleteSkipSetting 删除一个视频的跳过设置，记录不存在时不报错（幂等）。
func (s *Store) DeleteSkipSetting(videoID string) error {
	_, err := s.db.Exec(`DELETE FROM skip_settings WHERE video_id = ?`, videoID)
	return err
}
