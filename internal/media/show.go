package media

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Episode 是剧集中的一集，关联具体视频文件与其季/集序号。
type Episode struct {
	Video  *Video
	Season int
	Number int
}

// Season 是同一季下的有序集列表。
type Season struct {
	Number   int
	Episodes []*Episode
}

// Show 是按剧名/目录聚合的一部剧集，包含多季多集与展示用摘要信息。
type Show struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Year          string    `json:"year"`
	Dir           string    `json:"dir"`
	key           string    // 聚合键（目录/规范化剧名），不序列化，供增量重建定位
	Seasons       []*Season `json:"seasons"`
	EpisodeCount  int       `json:"episodeCount"`
	LatestModTime time.Time `json:"latestModTime"`
	PosterVideoID string    `json:"posterVideoID"` // 首个带本地/刮削海报的成员视频 ID，无则为空
}

// 季目录命名：Season 1 / Season01 / S01 / 第1季 / Specials（特辑，惯例对应第 0 季）
var seasonDirRE = regexp.MustCompile(`(?i)^(?:season\s*\d{1,2}|s\d{1,2}|specials?|特辑|番外)$|^第\s*[0-9零一二三四五六七八九十百]+\s*季$`)

// showDir 返回剧集聚合用的目录（相对路径，斜杠分隔）：
// 父目录是季目录（Season N / Sxx / 第N季）时取上两级，否则取视频所在目录。
func showDir(relPath string) string {
	dir := filepath.ToSlash(filepath.Dir(relPath))
	if dir == "." || dir == "/" {
		return ""
	}
	parts := strings.Split(dir, "/")
	parent := parts[len(parts)-1]
	if seasonDirRE.MatchString(parent) && len(parts) >= 2 {
		return strings.Join(parts[:len(parts)-1], "/")
	}
	return dir
}

// showKey 计算视频所属剧集的稳定键：聚合目录 + 规范化剧名。
// 解析不出季集标记的视频（电影）返回空串。
func showKey(v *Video) (string, EpisodeInfo, bool) {
	info, ok := ParseEpisode(v.Name)
	if !ok {
		return "", EpisodeInfo{}, false
	}
	return showDir(v.RelPath) + "/" + info.Title, info, true
}

// rebuildShowsLocked 全量重建剧集索引（要求调用方持有 mu 写锁）。
func (l *Library) rebuildShowsLocked() {
	shows := map[string]*Show{}
	videoShow := map[string]string{}

	groups := map[string][]*Episode{}
	for _, v := range l.videos {
		key, info, ok := showKey(v)
		if !ok {
			continue
		}
		groups[key] = append(groups[key], &Episode{Video: v, Season: info.Season, Number: info.Episode})
		videoShow[v.ID] = "" // 占位，随后回填真实 show ID
	}

	for key, episodes := range groups {
		show := buildShowLocked(l, key, episodes)
		shows[show.ID] = show
		for _, ep := range episodes {
			videoShow[ep.Video.ID] = show.ID
		}
	}
	l.shows = shows
	l.videoShow = videoShow
}

// rebuildShowLocked 按 key 重扫单个剧集的全部成员（要求调用方持有 mu 写锁）。
// 成员为 0（最后一集被删/改名迁走）时清除剧集与成员映射。
func (l *Library) rebuildShowLocked(key string) {
	if key == "" {
		return
	}
	var episodes []*Episode
	for _, v := range l.videos {
		k, info, ok := showKey(v)
		if !ok || k != key {
			continue
		}
		episodes = append(episodes, &Episode{Video: v, Season: info.Season, Number: info.Episode})
	}

	// 先移除该剧旧的成员映射（按 key 定位旧 ID，容忍 ID 算法外的历史数据）
	oldID := l.showIDByKeyLocked(key)
	for _, existing := range l.shows {
		if existing.key == key {
			oldID = existing.ID
			break
		}
	}
	for vid, sid := range l.videoShow {
		if sid == oldID {
			delete(l.videoShow, vid)
		}
	}
	if oldID != "" {
		delete(l.shows, oldID)
	}
	if len(episodes) == 0 {
		return
	}

	show := buildShowLocked(l, key, episodes)
	l.shows[show.ID] = show
	for _, ep := range episodes {
		l.videoShow[ep.Video.ID] = show.ID
	}
}

// showIDByKeyLocked 由 key 推导稳定 show ID（与 buildShowLocked 内的算法一致）。
func (l *Library) showIDByKeyLocked(key string) string {
	return makeID("show:" + key)
}

// showKeyByVideoLocked 返回当前索引中某视频所属剧集键；电影或视频不存在时返回空串。
func (l *Library) showKeyByVideoLocked(videoID string) string {
	v, ok := l.videos[videoID]
	if !ok {
		return ""
	}
	key, _, ok := showKey(v)
	if !ok {
		return ""
	}
	return key
}

// buildShowLocked 把同一 key 下的集列表组装为有序的 Show，并从成员元信息补齐海报/年份/剧名。
func buildShowLocked(l *Library, key string, episodes []*Episode) *Show {
	// 季、集均按序号升序；同集号按相对路径兜底稳定排序
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].Season != episodes[j].Season {
			return episodes[i].Season < episodes[j].Season
		}
		if episodes[i].Number != episodes[j].Number {
			return episodes[i].Number < episodes[j].Number
		}
		return episodes[i].Video.RelPath < episodes[j].Video.RelPath
	})

	seasonMap := map[int]*Season{}
	var seasonNums []int
	for _, ep := range episodes {
		s, ok := seasonMap[ep.Season]
		if !ok {
			s = &Season{Number: ep.Season}
			seasonMap[ep.Season] = s
			seasonNums = append(seasonNums, ep.Season)
		}
		s.Episodes = append(s.Episodes, ep)
	}
	sort.Ints(seasonNums)
	seasons := make([]*Season, 0, len(seasonNums))
	for _, n := range seasonNums {
		seasons = append(seasons, seasonMap[n])
	}

	dir, title, _ := strings.Cut(key, "/")
	show := &Show{
		ID:           makeID("show:" + key),
		Title:        title,
		Dir:          dir,
		key:          key,
		Seasons:      seasons,
		EpisodeCount: len(episodes),
	}

	var latest time.Time
	for _, ep := range episodes {
		v := ep.Video
		if v.ModTime.After(latest) {
			latest = v.ModTime
		}
		if show.PosterVideoID == "" {
			if md := l.metadata[v.ID]; md != nil && md.PosterPath != "" {
				show.PosterVideoID = v.ID
			}
		}
		if md := l.metadata[v.ID]; md != nil {
			if show.Year == "" && md.Year != "" {
				show.Year = md.Year
			}
			if md.Title != "" {
				show.Title = md.Title
			}
		}
	}
	show.LatestModTime = latest
	return show
}

// ListShows 返回按首集相对路径排序的全部剧集。
func (l *Library) ListShows() []*Show {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*Show, 0, len(l.shows))
	for _, s := range l.shows {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].firstRelPath() < out[j].firstRelPath()
	})
	return out
}

// firstRelPath 返回剧集首集的相对路径，用于稳定排序。
func (s *Show) firstRelPath() string {
	if len(s.Seasons) == 0 || len(s.Seasons[0].Episodes) == 0 {
		return ""
	}
	return s.Seasons[0].Episodes[0].Video.RelPath
}

// FirstEpisodeID 返回首集（季/集序最小）的视频 ID，供卡片缩略图兜底。
func (s *Show) FirstEpisodeID() string {
	if len(s.Seasons) == 0 || len(s.Seasons[0].Episodes) == 0 {
		return ""
	}
	return s.Seasons[0].Episodes[0].Video.ID
}

// GetShow 按 ID 查找剧集。
func (l *Library) GetShow(id string) (*Show, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s, ok := l.shows[id]
	return s, ok
}

// IsShowEpisode 判断视频是否属于某个剧集。
func (l *Library) IsShowEpisode(videoID string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.videoShow[videoID]
	return ok
}

// ShowVideos 返回剧集全部成员视频 ID 集合。
func (l *Library) ShowVideos(showID string) map[string]bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := map[string]bool{}
	for vid, sid := range l.videoShow {
		if sid == showID {
			out[vid] = true
		}
	}
	return out
}

// ShowIDOf 返回视频所属剧集 ID（不属于任何剧集时 ok=false）。
func (l *Library) ShowIDOf(videoID string) (string, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	id, ok := l.videoShow[videoID]
	return id, ok
}

// NextEpisode 返回同剧按季/集升序的下一集视频；电影或已是末集时 ok=false。
func (l *Library) NextEpisode(videoID string) (*Video, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	showID, ok := l.videoShow[videoID]
	if !ok {
		return nil, false
	}
	show := l.shows[showID]
	if show == nil {
		return nil, false
	}
	past := false
	for _, season := range show.Seasons {
		for _, ep := range season.Episodes {
			if past {
				return ep.Video, true
			}
			if ep.Video.ID == videoID {
				past = true
			}
		}
	}
	return nil, false
}
