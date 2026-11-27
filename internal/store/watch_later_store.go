package store

import (
	"fmt"
	"strings"
	"time"
)

// WatchLaterItem 是一条「稍后再看」记录，按加入时间倒序返回。
type WatchLaterItem struct {
	VideoID string    `json:"videoId"`
	AddedAt time.Time `json:"addedAt"`
}

// AddWatchLater 把视频加入稍后再看；已存在时保留原加入时间（DO NOTHING），不重复建行。
func (s *Store) AddWatchLater(videoID string) error {
	if videoID == "" {
		return fmt.Errorf("videoID 不能为空")
	}
	_, err := s.db.Exec(
		`INSERT INTO watch_later(video_id, added_at) VALUES(?, ?)
		 ON CONFLICT(video_id) DO NOTHING`,
		videoID, time.Now().UTC(),
	)
	return err
}

// RemoveWatchLater 把视频移出稍后再看；删除不存在的 ID 不报错（幂等）。
func (s *Store) RemoveWatchLater(videoID string) error {
	if videoID == "" {
		return fmt.Errorf("videoID 不能为空")
	}
	_, err := s.db.Exec(`DELETE FROM watch_later WHERE video_id = ?`, videoID)
	return err
}

// IsWatchLater 查询单个视频是否在稍后再看清单中。
func (s *Store) IsWatchLater(videoID string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(1) FROM watch_later WHERE video_id = ?`, videoID,
	).Scan(&n)
	return n > 0, err
}

// WatchLaterSet 返回全部稍后再看视频 ID 的集合，供列表/卡片批量渲染状态。
func (s *Store) WatchLaterSet() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT video_id FROM watch_later`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ListWatchLater 返回全部稍后再看记录（按加入时间倒序）。
func (s *Store) ListWatchLater() ([]WatchLaterItem, error) {
	rows, err := s.db.Query(
		`SELECT video_id, added_at FROM watch_later ORDER BY added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WatchLaterItem
	for rows.Next() {
		var it WatchLaterItem
		if err := rows.Scan(&it.VideoID, &it.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// RemoveWatchLaterByIDs 批量移出稍后再看（供「已看自动归档」复用），ID 列表为空时不执行。
func (s *Store) RemoveWatchLaterByIDs(videoIDs []string) error {
	if len(videoIDs) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(videoIDs))
	args := make([]any, 0, len(videoIDs))
	for _, id := range videoIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	_, err := s.db.Exec(
		`DELETE FROM watch_later WHERE video_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	return err
}
