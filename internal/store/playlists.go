package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// maxPlaylistNameLen 限制播放列表名称长度，防止异常超长输入。
const maxPlaylistNameLen = 100

// Playlist 是一个播放列表的表头及其有序条目（详情查询时 Items 非空）。
type Playlist struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	Items     []PlaylistItem `json:"items"`
}

// PlaylistItem 是播放列表中的一条成员，SortOrder 为 0 起的无间隙位置。
type PlaylistItem struct {
	PlaylistID string    `json:"playlistId"`
	VideoID    string    `json:"videoId"`
	SortOrder  int       `json:"sortOrder"`
	AddedAt    time.Time `json:"addedAt"`
}

// PlaylistSummary 是播放列表摘要，不含条目，供首页与选择弹窗使用。
type PlaylistSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	ItemCount int       `json:"itemCount"`
}

// newPlaylistID 用 crypto/rand 生成 32 位 hex ID，store 包不依赖 media 包的 makeID。
func newPlaylistID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// normalizePlaylistName 去首尾空白并校验，返回可用名称或错误。
func normalizePlaylistName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("播放列表名称不能为空")
	}
	if len(name) > maxPlaylistNameLen {
		return "", fmt.Errorf("播放列表名称不能超过 %d 个字符", maxPlaylistNameLen)
	}
	return name, nil
}

// ---------- 列表头 CRUD ----------

// CreatePlaylist 创建播放列表；ID 碰撞时最多重试 3 次。
func (s *Store) CreatePlaylist(name string) (*Playlist, error) {
	name, err := normalizePlaylistName(name)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var id string
	for attempt := 0; attempt < 3; attempt++ {
		id, err = newPlaylistID()
		if err != nil {
			return nil, err
		}
		var exists int
		if err = s.db.QueryRow(`SELECT COUNT(1) FROM playlists WHERE id = ?`, id).Scan(&exists); err != nil {
			return nil, err
		}
		if exists > 0 {
			continue // 极小概率碰撞，换一个 ID 重试
		}
		if _, err = s.db.Exec(
			`INSERT INTO playlists(id, name, created_at, updated_at) VALUES(?, ?, ?, ?)`,
			id, name, now, now); err != nil {
			return nil, err
		}
		return &Playlist{ID: id, Name: name, CreatedAt: now, UpdatedAt: now, Items: []PlaylistItem{}}, nil
	}
	return nil, fmt.Errorf("生成播放列表 ID 连续碰撞，请重试")
}

// RenamePlaylist 重命名；列表不存在返回 ErrNotFound。
func (s *Store) RenamePlaylist(id, name string) error {
	name, err := normalizePlaylistName(name)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE playlists SET name = ?, updated_at = ? WHERE id = ?`,
		name, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	return requireRowAffected(res)
}

// DeletePlaylist 事务内删除列表及其全部条目；不存在返回 ErrNotFound。
func (s *Store) DeletePlaylist(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`DELETE FROM playlists WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM playlist_item WHERE playlist_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// GetPlaylist 查询列表详情（含按 sort_order 排好的条目）；无记录返回 (nil, nil)。
func (s *Store) GetPlaylist(id string) (*Playlist, error) {
	pl := &Playlist{}
	err := s.db.QueryRow(
		`SELECT id, name, created_at, updated_at FROM playlists WHERE id = ?`, id,
	).Scan(&pl.ID, &pl.Name, &pl.CreatedAt, &pl.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT playlist_id, video_id, sort_order, added_at
		 FROM playlist_item WHERE playlist_id = ? ORDER BY sort_order`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pl.Items = make([]PlaylistItem, 0)
	for rows.Next() {
		var it PlaylistItem
		if err := rows.Scan(&it.PlaylistID, &it.VideoID, &it.SortOrder, &it.AddedAt); err != nil {
			return nil, err
		}
		pl.Items = append(pl.Items, it)
	}
	return pl, rows.Err()
}

// ListPlaylists 返回全部列表摘要（按创建时间升序，ItemCount 为条目数）。
func (s *Store) ListPlaylists() ([]PlaylistSummary, error) {
	rows, err := s.db.Query(
		`SELECT p.id, p.name, p.created_at, p.updated_at, COUNT(i.video_id)
		 FROM playlists p
		 LEFT JOIN playlist_item i ON i.playlist_id = p.id
		 GROUP BY p.id ORDER BY p.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlaylistSummary
	for rows.Next() {
		var sm PlaylistSummary
		if err := rows.Scan(&sm.ID, &sm.Name, &sm.CreatedAt, &sm.UpdatedAt, &sm.ItemCount); err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// ---------- 条目操作 ----------

// AddPlaylistItem 追加单个视频；已存在时幂等跳过并返回 (false, nil)。
// 列表不存在返回 ErrNotFound。
func (s *Store) AddPlaylistItem(playlistID, videoID string) (bool, error) {
	if videoID == "" {
		return false, fmt.Errorf("videoID 不能为空")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM playlists WHERE id = ?`, playlistID).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, ErrNotFound
	}
	var duplicated int
	if err := tx.QueryRow(
		`SELECT COUNT(1) FROM playlist_item WHERE playlist_id = ? AND video_id = ?`,
		playlistID, videoID).Scan(&duplicated); err != nil {
		return false, err
	}
	if duplicated > 0 {
		return false, tx.Commit()
	}
	var nextOrder sql.NullInt64
	if err := tx.QueryRow(
		`SELECT MAX(sort_order) + 1 FROM playlist_item WHERE playlist_id = ?`, playlistID,
	).Scan(&nextOrder); err != nil {
		return false, err
	}
	order := 0
	if nextOrder.Valid {
		order = int(nextOrder.Int64)
	}
	if _, err := tx.Exec(
		`INSERT INTO playlist_item(playlist_id, video_id, sort_order, added_at) VALUES(?, ?, ?, ?)`,
		playlistID, videoID, order, time.Now().UTC()); err != nil {
		return false, err
	}
	if err := touchPlaylist(tx, playlistID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// AddPlaylistItems 按 videoIDs 顺序批量追加（整剧一键加入用）；已存在的跳过且不占位，
// 返回实际新增条数。列表不存在返回 ErrNotFound。
func (s *Store) AddPlaylistItems(playlistID string, videoIDs []string) (int, error) {
	if len(videoIDs) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM playlists WHERE id = ?`, playlistID).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, ErrNotFound
	}
	// 已有成员建 set 去重
	rows, err := tx.Query(`SELECT video_id FROM playlist_item WHERE playlist_id = ?`, playlistID)
	if err != nil {
		return 0, err
	}
	known := map[string]bool{}
	for rows.Next() {
		var vid string
		if err := rows.Scan(&vid); err != nil {
			rows.Close()
			return 0, err
		}
		known[vid] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var maxOrder sql.NullInt64
	if err := tx.QueryRow(
		`SELECT MAX(sort_order) FROM playlist_item WHERE playlist_id = ?`, playlistID,
	).Scan(&maxOrder); err != nil {
		return 0, err
	}
	order := 0
	if maxOrder.Valid {
		order = int(maxOrder.Int64) + 1
	}
	now := time.Now().UTC()
	added := 0
	for _, vid := range videoIDs {
		if vid == "" || known[vid] {
			continue
		}
		// 逐行 INSERT 天然规避单语句占位符上限，单连接串行下性能足够
		if _, err := tx.Exec(
			`INSERT INTO playlist_item(playlist_id, video_id, sort_order, added_at) VALUES(?, ?, ?, ?)`,
			playlistID, vid, order, now); err != nil {
			return 0, err
		}
		known[vid] = true
		order++
		added++
	}
	if added > 0 {
		if err := touchPlaylist(tx, playlistID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}

// RemovePlaylistItem 移除条目并重排剩余项为连续 0..n-1；条目不存在不报错（幂等）。
func (s *Store) RemovePlaylistItem(playlistID, videoID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		`DELETE FROM playlist_item WHERE playlist_id = ? AND video_id = ?`, playlistID, videoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return tx.Commit() // 幂等：本就没有这条
	}
	if err := renumberItems(tx, playlistID); err != nil {
		return err
	}
	if err := touchPlaylist(tx, playlistID); err != nil {
		return err
	}
	return tx.Commit()
}

// MovePlaylistItem 把条目移动到 toIndex（0 起）；事务内读全序→内存移位→全量重编号。
// 条目不存在返回 ErrNotFound，toIndex 越界返回错误。
func (s *Store) MovePlaylistItem(playlistID, videoID string, toIndex int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(
		`SELECT video_id FROM playlist_item WHERE playlist_id = ? ORDER BY sort_order`, playlistID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var vid string
		if err := rows.Scan(&vid); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, vid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	from := -1
	for i, vid := range ids {
		if vid == videoID {
			from = i
			break
		}
	}
	if from < 0 {
		return ErrNotFound
	}
	if toIndex < 0 || toIndex >= len(ids) {
		return fmt.Errorf("目标位置 %d 越界（当前共 %d 条）", toIndex, len(ids))
	}
	if from != toIndex {
		// 内存中移动元素
		moving := ids[from]
		ids = append(ids[:from], ids[from+1:]...)
		ids = append(ids[:toIndex], append([]string{moving}, ids[toIndex:]...)...)
		for i, vid := range ids {
			if _, err := tx.Exec(
				`UPDATE playlist_item SET sort_order = ? WHERE playlist_id = ? AND video_id = ?`,
				i, playlistID, vid); err != nil {
				return err
			}
		}
		if err := touchPlaylist(tx, playlistID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PlaylistContains 查询视频是否已在指定列表中。
func (s *Store) PlaylistContains(playlistID, videoID string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(1) FROM playlist_item WHERE playlist_id = ? AND video_id = ?`,
		playlistID, videoID).Scan(&n)
	return n > 0, err
}

// VideoPlaylistMemberships 返回 视频 ID → 所属播放列表 ID 列表（按列表与位置排序）。
func (s *Store) VideoPlaylistMemberships() (map[string][]string, error) {
	rows, err := s.db.Query(
		`SELECT video_id, playlist_id FROM playlist_item ORDER BY playlist_id, sort_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var vid, pid string
		if err := rows.Scan(&vid, &pid); err != nil {
			return nil, err
		}
		out[vid] = append(out[vid], pid)
	}
	return out, rows.Err()
}

// ---------- 事务内辅助 ----------

// renumberItems 把列表内条目重排为连续的 0..n-1。
func renumberItems(tx *sql.Tx, playlistID string) error {
	rows, err := tx.Query(
		`SELECT video_id FROM playlist_item WHERE playlist_id = ? ORDER BY sort_order`, playlistID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var vid string
		if err := rows.Scan(&vid); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, vid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i, vid := range ids {
		if _, err := tx.Exec(
			`UPDATE playlist_item SET sort_order = ? WHERE playlist_id = ? AND video_id = ?`,
			i, playlistID, vid); err != nil {
			return err
		}
	}
	return nil
}

// touchPlaylist 刷新列表的 updated_at。
func touchPlaylist(tx *sql.Tx, playlistID string) error {
	_, err := tx.Exec(`UPDATE playlists SET updated_at = ? WHERE id = ?`, time.Now().UTC(), playlistID)
	return err
}

// requireRowAffected 把零行更新映射为 ErrNotFound。
func requireRowAffected(res sql.Result) error {
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}
