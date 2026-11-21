package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// maxBookmarkNoteRunes 限制单条书签备注的最大字符数（按 rune 截断，不切坏中文）。
const maxBookmarkNoteRunes = 500

// Bookmark 是单个视频时间轴上的一条时间戳书签。
type Bookmark struct {
	ID        int64     `json:"id"`
	VideoID   string    `json:"videoId"`
	Position  float64   `json:"position"` // 书签位置，单位秒
	Note      string    `json:"note"`     // 备注，可为空字符串
	CreatedAt time.Time `json:"createdAt"`
}

// bookmarkColumns 集中维护 bookmarks 的完整列，避免多处手写漂移。
const bookmarkColumns = `id, video_id, position, note, created_at`

// normalizeNote 去除首尾空白并按 rune 截断到上限，避免超长备注撑爆请求与面板。
func normalizeNote(note string) string {
	note = strings.TrimSpace(note)
	runes := []rune(note)
	if len(runes) > maxBookmarkNoteRunes {
		note = string(runes[:maxBookmarkNoteRunes])
	}
	return note
}

// CreateBookmark 在指定视频上创建一条书签并回读完整行。
// 负位置在 store 层兜底归零，与 SaveProgress/UpsertSkipSetting 的钳制风格保持一致。
func (s *Store) CreateBookmark(videoID string, position float64, note string) (*Bookmark, error) {
	if videoID == "" {
		return nil, fmt.Errorf("videoID 不能为空")
	}
	if position < 0 {
		position = 0
	}
	note = normalizeNote(note)
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO bookmarks(video_id, position, note, created_at) VALUES(?, ?, ?, ?)`,
		videoID, position, note, now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.getBookmarkByID(id)
}

// getBookmarkByID 按主键回读一条书签，列顺序须与 bookmarkColumns 一致。
func (s *Store) getBookmarkByID(id int64) (*Bookmark, error) {
	row := s.db.QueryRow(
		`SELECT `+bookmarkColumns+` FROM bookmarks WHERE id = ?`, id,
	)
	bm, err := scanBookmark(row)
	if err != nil {
		return nil, err
	}
	return &bm, nil
}

// scanBookmark 统一收口一行 bookmarks 的扫描。
func scanBookmark(scanner interface{ Scan(...any) error }) (Bookmark, error) {
	var bm Bookmark
	err := scanner.Scan(&bm.ID, &bm.VideoID, &bm.Position, &bm.Note, &bm.CreatedAt)
	return bm, err
}

// ListBookmarks 列出一个视频的全部书签，按位置升序、同位置按 id 升序。
// 无记录时返回 (nil, nil)，与 GetProgress/GetSkipSetting 的无记录契约一致。
func (s *Store) ListBookmarks(videoID string) ([]Bookmark, error) {
	rows, err := s.db.Query(
		`SELECT `+bookmarkColumns+` FROM bookmarks WHERE video_id = ?
		 ORDER BY position ASC, id ASC`,
		videoID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Bookmark
	for rows.Next() {
		bm, err := scanBookmark(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, bm)
	}
	return out, rows.Err()
}

// UpdateBookmarkNote 修改一条书签的备注。videoID 作为归属条件防止跨视频误改：
// 命中返回 (true, nil)，书签不存在或不属于该视频返回 (false, nil)。
func (s *Store) UpdateBookmarkNote(id int64, videoID string, note string) (bool, error) {
	note = normalizeNote(note)
	res, err := s.db.Exec(
		`UPDATE bookmarks SET note = ? WHERE id = ? AND video_id = ?`,
		note, id, videoID,
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// GetBookmark 按主键与归属视频回读一条书签，不存在返回 (nil, nil)。
func (s *Store) GetBookmark(id int64, videoID string) (*Bookmark, error) {
	row := s.db.QueryRow(
		`SELECT `+bookmarkColumns+` FROM bookmarks WHERE id = ? AND video_id = ?`,
		id, videoID,
	)
	bm, err := scanBookmark(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &bm, nil
}

// DeleteBookmark 删除一条书签，带 videoID 归属条件；记录不存在时不报错（幂等）。
func (s *Store) DeleteBookmark(id int64, videoID string) error {
	_, err := s.db.Exec(
		`DELETE FROM bookmarks WHERE id = ? AND video_id = ?`, id, videoID,
	)
	return err
}
