// Package store 基于 SQLite 的持久化层，保存播放进度与收藏。
// 使用纯 Go 驱动 modernc.org/sqlite，无需 CGO，便于交叉编译与 NAS 容器部署。
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Progress 是一条播放进度记录。
type Progress struct {
	VideoID   string    `json:"videoId"`
	Position  float64   `json:"position"` // 播放位置（秒）
	Duration  float64   `json:"duration"` // 视频总时长（秒），未知时为 0
	UpdatedAt time.Time `json:"updatedAt"`
}

// Favorite 是一条收藏记录。
type Favorite struct {
	VideoID string    `json:"videoId"`
	AddedAt time.Time `json:"addedAt"`
}

// Store 封装 SQLite 连接，并发安全由 database/sql 保证。
type Store struct {
	db *sql.DB
}

// Open 打开（或创建）path 处的 SQLite 数据库并初始化表结构。
func Open(path string) (*Store, error) {
	// _txlock=immediate 让写事务立即获取 RESERVED 锁，减少 SQLITE_BUSY
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// 单连接写、多读；modernc 驱动下串行写最稳妥，避免 database is locked
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS progress (
    video_id   TEXT PRIMARY KEY,
    position   REAL NOT NULL DEFAULT 0,
    duration   REAL NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS favorites (
    video_id TEXT PRIMARY KEY,
    added_at TIMESTAMP NOT NULL
);
`)
	return err
}

// Close 关闭数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// ---------- 播放进度 ----------

// SaveProgress 上报播放进度。position/duration 为负数时按 0 处理。
func (s *Store) SaveProgress(videoID string, position, duration float64) error {
	if videoID == "" {
		return fmt.Errorf("videoID 不能为空")
	}
	if position < 0 {
		position = 0
	}
	if duration < 0 {
		duration = 0
	}
	_, err := s.db.Exec(
		`INSERT INTO progress(video_id, position, duration, updated_at)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(video_id) DO UPDATE SET
		   position=excluded.position,
		   duration=excluded.duration,
		   updated_at=excluded.updated_at`,
		videoID, position, duration, time.Now().UTC(),
	)
	return err
}

// GetProgress 查询单个视频的播放进度，无记录时返回 nil。
func (s *Store) GetProgress(videoID string) (*Progress, error) {
	row := s.db.QueryRow(
		`SELECT video_id, position, duration, updated_at FROM progress WHERE video_id = ?`,
		videoID,
	)
	p := &Progress{}
	if err := row.Scan(&p.VideoID, &p.Position, &p.Duration, &p.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// ProgressMap 批量查询进度，返回 videoID -> Progress，供列表页一次取全。
func (s *Store) ProgressMap() (map[string]Progress, error) {
	rows, err := s.db.Query(`SELECT video_id, position, duration, updated_at FROM progress`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Progress)
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.VideoID, &p.Position, &p.Duration, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out[p.VideoID] = p
	}
	return out, rows.Err()
}

// RecentProgress 返回最近观看的进度记录（按更新时间倒序），limit <= 0 时默认 20。
func (s *Store) RecentProgress(limit int) ([]Progress, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT video_id, position, duration, updated_at
		 FROM progress ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Progress
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.VideoID, &p.Position, &p.Duration, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProgress 删除一条播放进度（如「清除记录」）。
func (s *Store) DeleteProgress(videoID string) error {
	_, err := s.db.Exec(`DELETE FROM progress WHERE video_id = ?`, videoID)
	return err
}

// ---------- 收藏 ----------

// SetFavorite 收藏(true)或取消收藏(false)一个视频。
func (s *Store) SetFavorite(videoID string, fav bool) error {
	if videoID == "" {
		return fmt.Errorf("videoID 不能为空")
	}
	if fav {
		_, err := s.db.Exec(
			`INSERT INTO favorites(video_id, added_at) VALUES(?, ?)
			 ON CONFLICT(video_id) DO NOTHING`,
			videoID, time.Now().UTC(),
		)
		return err
	}
	_, err := s.db.Exec(`DELETE FROM favorites WHERE video_id = ?`, videoID)
	return err
}

// IsFavorite 查询单个视频是否已收藏。
func (s *Store) IsFavorite(videoID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM favorites WHERE video_id = ?`, videoID).Scan(&n)
	return n > 0, err
}

// FavoriteSet 返回全部已收藏视频 ID 的集合。
func (s *Store) FavoriteSet() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT video_id FROM favorites`)
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

// ListFavorites 返回全部收藏记录（按收藏时间倒序）。
func (s *Store) ListFavorites() ([]Favorite, error) {
	rows, err := s.db.Query(
		`SELECT video_id, added_at FROM favorites ORDER BY added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Favorite
	for rows.Next() {
		var f Favorite
		if err := rows.Scan(&f.VideoID, &f.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---------- 维护 ----------

// Prune 删除指向已不存在视频的孤儿记录（全量重扫后可调用）。
// existing 为当前索引中存在的视频 ID 集合。
func (s *Store) Prune(existing map[string]bool) error {
	if len(existing) == 0 {
		if _, err := s.db.Exec(`DELETE FROM progress`); err != nil {
			return err
		}
		_, err := s.db.Exec(`DELETE FROM favorites`)
		return err
	}

	placeholders := make([]string, 0, len(existing))
	args := make([]any, 0, len(existing))
	for id := range existing {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	inClause := strings.Join(placeholders, ",")
	if _, err := s.db.Exec(
		`DELETE FROM progress WHERE video_id NOT IN (`+inClause+`)`, args...); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`DELETE FROM favorites WHERE video_id NOT IN (`+inClause+`)`, args...)
	return err
}
