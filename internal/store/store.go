// Package store 基于 SQLite 的持久化层，保存播放进度与收藏。
// 使用纯 Go 驱动 modernc.org/sqlite，无需 CGO，便于交叉编译与 NAS 容器部署。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound 表示按 ID 查找的记录不存在，handler 层据此映射 404。
var ErrNotFound = errors.New("记录不存在")

// Progress 是一条播放进度记录。
type Progress struct {
	VideoID          string    `json:"videoId"`
	Position         float64   `json:"position"`         // 播放位置（秒）
	Duration         float64   `json:"duration"`         // 视频总时长（秒），未知时为 0
	WatchTimeSeconds float64   `json:"watchTimeSeconds"` // 累计观看时长（秒），多次观看增量累加
	PlayCount        int64     `json:"playCount"`        // 播放次数（每次播放会话首次上报 +1）
	Completed        bool      `json:"completed"`        // 是否看完（>=95%），黏滞，置真后不回退
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ProgressUpdate 是一次播放进度上报的入参，由 handler 组装后交给 SaveProgress。
type ProgressUpdate struct {
	Position     float64 // 当前播放位置（秒）
	Duration     float64 // 视频总时长（秒），未知时为 0
	WatchDelta   float64 // 本次上报距上次成功上报之间新增的观看秒数
	SessionStart bool    // 是否为本次播放会话的首次上报（用于累计播放次数）
	Completed    bool    // 本次上报是否达到看完阈值（store 仅做黏滞存储，阈值在 handler 判定）
}

// WatchStats 是观影统计页顶部的聚合数字。
type WatchStats struct {
	TotalWatchSeconds float64 // 全部视频累计观看时长（秒）
	WatchedVideos     int64   // 有进度记录的去重视频数
	CompletedVideos   int64   // 已看完的视频数
	TotalPlays        int64   // 全部视频累计播放次数
	FavoriteCount     int64   // 收藏视频数
}

// WatchEvent 是一次播放会话的历史记录：会话首次上报建一行，同会话后续上报累加。
type WatchEvent struct {
	ID           int64     `json:"id"`
	VideoID      string    `json:"videoId"`
	StartedAt    time.Time `json:"startedAt"` // 会话开始时间（UTC）
	UpdatedAt    time.Time `json:"updatedAt"` // 最近一次上报时间（UTC）
	WatchSeconds float64   `json:"watchSeconds"`
	Position     float64   `json:"position"`
	Duration     float64   `json:"duration"`
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
    video_id            TEXT PRIMARY KEY,
    position            REAL NOT NULL DEFAULT 0,
    duration            REAL NOT NULL DEFAULT 0,
    watch_time_seconds  REAL NOT NULL DEFAULT 0,
    play_count          INTEGER NOT NULL DEFAULT 0,
    completed           INTEGER NOT NULL DEFAULT 0,
    updated_at          TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS favorites (
    video_id TEXT PRIMARY KEY,
    added_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS playlists (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS playlist_item (
    playlist_id TEXT    NOT NULL,
    video_id    TEXT    NOT NULL,
    sort_order  INTEGER NOT NULL,
    added_at    TIMESTAMP NOT NULL,
    PRIMARY KEY (playlist_id, video_id)
);
CREATE INDEX IF NOT EXISTS idx_playlist_item_video ON playlist_item(video_id);
CREATE INDEX IF NOT EXISTS idx_playlist_item_order ON playlist_item(playlist_id, sort_order);
CREATE TABLE IF NOT EXISTS shares (
    token         TEXT PRIMARY KEY,
    video_id      TEXT NOT NULL,
    passcode_hash TEXT NOT NULL DEFAULT '',
    expires_at    TIMESTAMP NOT NULL,
    created_at    TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_shares_video ON shares(video_id);
CREATE INDEX IF NOT EXISTS idx_shares_expires ON shares(expires_at);
CREATE TABLE IF NOT EXISTS watch_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    video_id      TEXT NOT NULL,
    started_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL,
    watch_seconds REAL NOT NULL DEFAULT 0,
    position      REAL NOT NULL DEFAULT 0,
    duration      REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_watch_events_started ON watch_events(started_at DESC);
CREATE TABLE IF NOT EXISTS skip_settings (
    video_id      TEXT PRIMARY KEY,
    intro_seconds REAL NOT NULL DEFAULT 0,
    outro_seconds REAL NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    updated_at    TIMESTAMP NOT NULL
);
`)
	if err != nil {
		return err
	}
	// 老库升级：CREATE TABLE IF NOT EXISTS 不会改已存在的表，需逐列补齐（幂等）。
	// SetMaxOpenConns(1) 串行执行，无并发 DDL 风险。
	for _, c := range []struct{ name, ddlType string }{
		{"watch_time_seconds", "REAL NOT NULL DEFAULT 0"},
		{"play_count", "INTEGER NOT NULL DEFAULT 0"},
		{"completed", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.addColumnIfNotExists("progress", c.name, c.ddlType); err != nil {
			return err
		}
	}
	return nil
}

// addColumnIfNotExists 在 table 缺少 column 时执行 ALTER TABLE 补列，已存在则跳过。
func (s *Store) addColumnIfNotExists(table, column, ddlType string) error {
	var dummy int
	err := s.db.QueryRow(
		`SELECT 1 FROM pragma_table_info(?) WHERE name = ?`, table, column,
	).Scan(&dummy)
	if err == nil {
		return nil // 列已存在
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddlType))
	return err
}

// Close 关闭数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层连接，仅供同模块测试构造特殊数据（如手动置过期）使用。
func (s *Store) DB() *sql.DB { return s.db }

// ---------- 播放进度 ----------

// progressColumns 是 progress 表的完整列，集中维护避免多处手写漂移。
const progressColumns = `video_id, position, duration, watch_time_seconds, play_count, completed, updated_at`

// SaveProgress 上报一次播放进度。
// 位置/时长为覆盖式更新；观看时长按 WatchDelta 累加；播放次数在 SessionStart 时 +1；
// completed 取历史与本次的较大值（黏滞，置真后不回退）。各数值字段为负时按 0 处理。
func (s *Store) SaveProgress(videoID string, u ProgressUpdate) error {
	if videoID == "" {
		return fmt.Errorf("videoID 不能为空")
	}
	if u.Position < 0 {
		u.Position = 0
	}
	if u.Duration < 0 {
		u.Duration = 0
	}
	if u.WatchDelta < 0 {
		u.WatchDelta = 0
	}
	// 新行的初始播放次数：会话开始记 1，否则 0
	insertPlayCount := int64(0)
	if u.SessionStart {
		insertPlayCount = 1
	}
	completed := 0
	if u.Completed {
		completed = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO progress(video_id, position, duration, watch_time_seconds, play_count, completed, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(video_id) DO UPDATE SET
		   position=excluded.position,
		   duration=excluded.duration,
		   watch_time_seconds=progress.watch_time_seconds + excluded.watch_time_seconds,
		   play_count=progress.play_count + ?,
		   completed=MAX(progress.completed, excluded.completed),
		   updated_at=excluded.updated_at`,
		videoID, u.Position, u.Duration, u.WatchDelta, insertPlayCount, completed, time.Now().UTC(),
		// 冲突分支的播放次数增量：会话开始 +1，否则 +0（同一 SessionStart 语义在此复用一次）
		insertPlayCount,
	)
	return err
}

// scanProgress 统一收口一行 progress 的扫描，列顺序须与 progressColumns 一致。
func scanProgress(scanner interface{ Scan(...any) error }) (Progress, error) {
	var p Progress
	var completed int
	if err := scanner.Scan(&p.VideoID, &p.Position, &p.Duration,
		&p.WatchTimeSeconds, &p.PlayCount, &completed, &p.UpdatedAt); err != nil {
		return Progress{}, err
	}
	p.Completed = completed != 0
	return p, nil
}

// GetProgress 查询单个视频的播放进度，无记录时返回 nil。
func (s *Store) GetProgress(videoID string) (*Progress, error) {
	row := s.db.QueryRow(
		`SELECT `+progressColumns+` FROM progress WHERE video_id = ?`,
		videoID,
	)
	p, err := scanProgress(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// ProgressMap 批量查询进度，返回 videoID -> Progress，供列表页一次取全。
func (s *Store) ProgressMap() (map[string]Progress, error) {
	rows, err := s.db.Query(`SELECT ` + progressColumns + ` FROM progress`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Progress)
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
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
		`SELECT `+progressColumns+` FROM progress ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Progress
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CompletedProgress 返回已看完（completed=1）的进度记录（按更新时间倒序），limit <= 0 时默认 20。
func (s *Store) CompletedProgress(limit int) ([]Progress, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT `+progressColumns+` FROM progress WHERE completed = 1
		 ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Progress
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// WatchStats 聚合观影统计页顶部数字：progress 一条查询取四项，收藏数另查 favorites。
func (s *Store) WatchStats() (WatchStats, error) {
	var st WatchStats
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(watch_time_seconds), 0),
		        COUNT(1),
		        COALESCE(SUM(completed), 0),
		        COALESCE(SUM(play_count), 0)
		 FROM progress`,
	).Scan(&st.TotalWatchSeconds, &st.WatchedVideos, &st.CompletedVideos, &st.TotalPlays)
	if err != nil {
		return WatchStats{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM favorites`).Scan(&st.FavoriteCount); err != nil {
		return WatchStats{}, err
	}
	return st, nil
}

// DeleteProgress 删除一条播放进度（如「清除记录」）。
func (s *Store) DeleteProgress(videoID string) error {
	_, err := s.db.Exec(`DELETE FROM progress WHERE video_id = ?`, videoID)
	return err
}

// ---------- 观看历史（会话级事件） ----------

// StartWatchEvent 开启一次播放会话并写入一行事件，返回事件 ID。
func (s *Store) StartWatchEvent(videoID string, at time.Time) (int64, error) {
	if videoID == "" {
		return 0, fmt.Errorf("videoID 不能为空")
	}
	res, err := s.db.Exec(
		`INSERT INTO watch_events(video_id, started_at, updated_at) VALUES(?, ?, ?)`,
		videoID, at, at,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateWatchEvent 累加同一会话的观看秒数并刷新位置与更新时间。
// 事件 ID 不存在（影响 0 行）时不报错，由调用方决定是否补建。
func (s *Store) UpdateWatchEvent(id int64, watchDelta, position, duration float64, at time.Time) error {
	if watchDelta < 0 {
		watchDelta = 0
	}
	if position < 0 {
		position = 0
	}
	if duration < 0 {
		duration = 0
	}
	_, err := s.db.Exec(
		`UPDATE watch_events
		 SET watch_seconds = watch_seconds + ?, position = ?, duration = ?, updated_at = ?
		 WHERE id = ?`,
		watchDelta, position, duration, at, id,
	)
	return err
}

// ListWatchEvents 按会话开始时间倒序返回观看事件，limit <= 0 时默认 100。
func (s *Store) ListWatchEvents(limit int) ([]WatchEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, video_id, started_at, updated_at, watch_seconds, position, duration
		 FROM watch_events ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WatchEvent
	for rows.Next() {
		var e WatchEvent
		if err := rows.Scan(&e.ID, &e.VideoID, &e.StartedAt, &e.UpdatedAt,
			&e.WatchSeconds, &e.Position, &e.Duration); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClearWatchEvents 清空全部观看历史。
func (s *Store) ClearWatchEvents() error {
	_, err := s.db.Exec(`DELETE FROM watch_events`)
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
		if _, err := s.db.Exec(`DELETE FROM favorites`); err != nil {
			return err
		}
		// 只清条目、保留 playlists 表头：空合集仍由用户显式删除
		if _, err := s.db.Exec(`DELETE FROM playlist_item`); err != nil {
			return err
		}
		_, err := s.db.Exec(`DELETE FROM skip_settings`)
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
	if _, err := s.db.Exec(
		`DELETE FROM favorites WHERE video_id NOT IN (`+inClause+`)`, args...); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`DELETE FROM playlist_item WHERE video_id NOT IN (`+inClause+`)`, args...); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`DELETE FROM skip_settings WHERE video_id NOT IN (`+inClause+`)`, args...)
	return err
}
