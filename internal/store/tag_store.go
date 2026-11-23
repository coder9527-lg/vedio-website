package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// maxTagNameRunes 限制单个标签名的最大字符数（按 rune 计，不切坏中文）。
const maxTagNameRunes = 32

// ErrInvalidTag 表示标签名不合法（空串或超长），handler 层据此映射 400。
var ErrInvalidTag = errors.New("标签名不合法")

// Tag 是一个用户自定义标签；VideoCount 仅在 ListTags 聚合时填充。
type Tag struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	VideoCount int       `json:"videoCount,omitempty"`
}

// tagColumns 集中维护 tags 表的基础列，避免多处手写漂移。
const tagColumns = `id, name, created_at`

// normalizeTag 去首尾空白并校验标签名，返回可用名称或错误。
// 长度按 rune 计，中文等多字节字符按一个字计算。
func normalizeTag(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: 标签名不能为空", ErrInvalidTag)
	}
	if len([]rune(name)) > maxTagNameRunes {
		return "", fmt.Errorf("%w: 标签名不能超过 %d 个字符", ErrInvalidTag, maxTagNameRunes)
	}
	return name, nil
}

// ensureTagLocked 在事务内按小写名查找标签，不存在则插入，返回标签 ID 与是否新建。
// 唯一索引建在 LOWER(name) 上，故 Movie/movie 命中同一行；展示保留首次写入的大小写。
func ensureTagLocked(tx *sql.Tx, name string, now time.Time) (int64, bool, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM tags WHERE LOWER(name) = LOWER(?)`, name).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	res, err := tx.Exec(`INSERT INTO tags(name, created_at) VALUES(?, ?)`, name, now)
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// scanTag 扫描一行 tags 基础列，列顺序须与 tagColumns 一致。
func scanTag(scanner interface{ Scan(...any) error }) (Tag, error) {
	var tg Tag
	err := scanner.Scan(&tg.ID, &tg.Name, &tg.CreatedAt)
	return tg, err
}

// getTagByIDLocked 在事务内按主键回读标签基础信息。
func getTagByIDLocked(tx *sql.Tx, id int64) (*Tag, error) {
	row := tx.QueryRow(`SELECT `+tagColumns+` FROM tags WHERE id = ?`, id)
	tg, err := scanTag(row)
	if err != nil {
		return nil, err
	}
	return &tg, nil
}

// SetVideoTag 给视频打标签：标签不存在则自动创建，已打过则幂等跳过。
// 返回标签内容与 created（true 表示本次新建了标签或新建了关联）。
func (s *Store) SetVideoTag(videoID, name string) (*Tag, bool, error) {
	if videoID == "" {
		return nil, false, fmt.Errorf("videoID 不能为空")
	}
	name, err := normalizeTag(name)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	tagID, tagCreated, err := ensureTagLocked(tx, name, now)
	if err != nil {
		return nil, false, err
	}
	// 关联表复合主键去重：已存在时 INSERT OR IGNORE 不产生新行，保持幂等
	res, err := tx.Exec(
		`INSERT OR IGNORE INTO video_tag(tag_id, video_id, added_at) VALUES(?, ?, ?)`,
		tagID, videoID, now)
	if err != nil {
		return nil, false, err
	}
	linkCreated := false
	if n, _ := res.RowsAffected(); n > 0 {
		linkCreated = true
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	tg, err := s.tagByID(tagID)
	if err != nil {
		return nil, false, err
	}
	// 标签已存在但本视频首次打标，对调用方而言仍是一次新增
	return tg, tagCreated || linkCreated, nil
}

// tagByID 在事务外按主键回读标签。
func (s *Store) tagByID(id int64) (*Tag, error) {
	row := s.db.QueryRow(`SELECT `+tagColumns+` FROM tags WHERE id = ?`, id)
	tg, err := scanTag(row)
	if err != nil {
		return nil, err
	}
	return &tg, nil
}

// RemoveVideoTag 取消视频的某个标签；取消后该标签若无任何视频引用则同事务自动回收。
// 关联本就不存在时不报错（幂等，与 RemovePlaylistItem 口径一致）。
func (s *Store) RemoveVideoTag(videoID string, tagID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`DELETE FROM video_tag WHERE tag_id = ? AND video_id = ?`, tagID, videoID); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(
		`SELECT COUNT(1) FROM video_tag WHERE tag_id = ?`, tagID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := tx.Exec(`DELETE FROM tags WHERE id = ?`, tagID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// VideoTags 返回某视频的全部标签，按打标时间升序；无标签时返回非 nil 空切片。
func (s *Store) VideoTags(videoID string) ([]Tag, error) {
	rows, err := s.db.Query(
		`SELECT t.id, t.name, t.created_at
		 FROM tags t JOIN video_tag vt ON vt.tag_id = t.id
		 WHERE vt.video_id = ?
		 ORDER BY vt.added_at ASC, t.id ASC`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTagRows(rows)
}

// VideoTagsMap 一次查询取回全部视频的标签映射（视频 ID → 标签列表），供列表页避免 N+1。
func (s *Store) VideoTagsMap() (map[string][]Tag, error) {
	rows, err := s.db.Query(
		`SELECT vt.video_id, t.id, t.name, t.created_at
		 FROM video_tag vt JOIN tags t ON t.id = vt.tag_id
		 ORDER BY vt.added_at ASC, t.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]Tag{}
	for rows.Next() {
		var videoID string
		var tg Tag
		if err := rows.Scan(&videoID, &tg.ID, &tg.Name, &tg.CreatedAt); err != nil {
			return nil, err
		}
		out[videoID] = append(out[videoID], tg)
	}
	return out, rows.Err()
}

// ListTags 返回全部标签及其引用视频数，按名称排序；空库返回非 nil 空切片。
func (s *Store) ListTags() ([]Tag, error) {
	rows, err := s.db.Query(
		`SELECT t.id, t.name, t.created_at, COUNT(vt.video_id)
		 FROM tags t LEFT JOIN video_tag vt ON vt.tag_id = t.id
		 GROUP BY t.id
		 ORDER BY t.name COLLATE NOCASE ASC, t.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Tag, 0)
	for rows.Next() {
		var tg Tag
		if err := rows.Scan(&tg.ID, &tg.Name, &tg.CreatedAt, &tg.VideoCount); err != nil {
			return nil, err
		}
		out = append(out, tg)
	}
	return out, rows.Err()
}

// TagByName 按名称（大小写不敏感）精确查找标签；无记录返回 (nil, nil)。
func (s *Store) TagByName(name string) (*Tag, error) {
	row := s.db.QueryRow(
		`SELECT `+tagColumns+` FROM tags WHERE LOWER(name) = LOWER(?)`, name)
	tg, err := scanTag(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &tg, nil
}

// TagVideoSet 返回引用某标签的视频 ID 集合；标签不存在时返回 ErrNotFound。
func (s *Store) TagVideoSet(tagID int64) (map[string]bool, error) {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM tags WHERE id = ?`, tagID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(`SELECT video_id FROM video_tag WHERE tag_id = ?`, tagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var vid string
		if err := rows.Scan(&vid); err != nil {
			return nil, err
		}
		out[vid] = true
	}
	return out, rows.Err()
}

// scanTagRows 收口标签行的扫描循环，保证各查询返回空切片而非 nil。
func scanTagRows(rows *sql.Rows) ([]Tag, error) {
	out := make([]Tag, 0)
	for rows.Next() {
		tg, err := scanTag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tg)
	}
	return out, rows.Err()
}
