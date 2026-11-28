package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Rating 是一条个人评分记录，stars 取值 1-5。
type Rating struct {
	VideoID   string    `json:"videoId"`
	Stars     int       `json:"stars"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ratingColumns 集中维护 ratings 表列顺序，扫描处与查询保持一致。
const ratingColumns = `video_id, stars, updated_at`

// validStars 校验评分档位，仅允许 1-5 星。
func validStars(stars int) bool { return stars >= 1 && stars <= 5 }

// SetRating 覆盖式写入某视频的 1-5 星评分；非法档位返回错误。
func (s *Store) SetRating(videoID string, stars int) error {
	if videoID == "" {
		return fmt.Errorf("videoID 不能为空")
	}
	if !validStars(stars) {
		return fmt.Errorf("stars 必须在 1-5 之间，收到 %d", stars)
	}
	_, err := s.db.Exec(
		`INSERT INTO ratings(video_id, stars, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(video_id) DO UPDATE SET stars=excluded.stars, updated_at=excluded.updated_at`,
		videoID, stars, time.Now().UTC(),
	)
	return err
}

// DeleteRating 清除某视频的评分（无记录时不报错，与 DeleteProgress 容忍策略一致）。
func (s *Store) DeleteRating(videoID string) error {
	_, err := s.db.Exec(`DELETE FROM ratings WHERE video_id = ?`, videoID)
	return err
}

// GetRating 查询单个视频的评分，无记录返回 nil。
func (s *Store) GetRating(videoID string) (*Rating, error) {
	row := s.db.QueryRow(
		`SELECT `+ratingColumns+` FROM ratings WHERE video_id = ?`, videoID)
	r, err := scanRating(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// RatingMap 批量查询评分，返回 videoID -> Rating，供列表/详情页一次取全。
func (s *Store) RatingMap() (map[string]Rating, error) {
	rows, err := s.db.Query(`SELECT ` + ratingColumns + ` FROM ratings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Rating)
	for rows.Next() {
		r, err := scanRating(rows)
		if err != nil {
			return nil, err
		}
		out[r.VideoID] = r
	}
	return out, rows.Err()
}

// HighRated 返回评分达到 minStars 的记录，按星级倒序、同级按评分时间倒序，
// limit <= 0 时默认 50。供首页「我的高分」智能视图使用。
func (s *Store) HighRated(minStars, limit int) ([]Rating, error) {
	if !validStars(minStars) {
		minStars = 4
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT `+ratingColumns+` FROM ratings WHERE stars >= ?
		 ORDER BY stars DESC, updated_at DESC LIMIT ?`, minStars, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rating
	for rows.Next() {
		r, err := scanRating(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// scanRating 统一收口一行 ratings 的扫描。
func scanRating(scanner interface{ Scan(...any) error }) (Rating, error) {
	var r Rating
	err := scanner.Scan(&r.VideoID, &r.Stars, &r.UpdatedAt)
	return r, err
}
