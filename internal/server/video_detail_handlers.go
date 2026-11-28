package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// similarSize 限制详情页「更多类似」推荐卡片数量。
const similarSize = 12

// detailCard 是详情页推荐位的单张卡片，复用首页卡片的海报回退规则。
type detailCard struct {
	Video    *media.Video
	Metadata *media.Metadata
	Reason   string // 推荐理由：同剧集 / 共同类型 / 共同标签 / 同目录
}

// detailProgress 是详情页进度条的预计算字段，模板侧不做算术。
type detailProgress struct {
	Available bool
	Position  float64
	Duration  float64
	Percent   int
	Remain    float64
}

// videoDetailPageData 是影片详情页模板数据。
type videoDetailPageData struct {
	basePage
	Video     *media.Video
	Metadata  *media.Metadata // 无刮削元信息时为零值结构体（非 nil），模板直接判字段
	StoreOn   bool
	ShowID    string // 所属剧集 ID（电影为空串）
	ShowTitle string // 所属剧集名称（用于「返回剧集」入口）
	Fav       bool
	WatchLater bool
	Tags      []store.Tag
	MyStars   int // 个人评分 1-5，未评分/未持久化为 0
	Progress  detailProgress
	Similar   []detailCard
	HasMeta   bool // 是否存在任何刮削元信息，控制整块元信息区的空态文案
}

// handleVideoDetail 渲染独立影片详情页 /video/{id}：
// 聚合文件信息、刮削元信息、播放进度、收藏/稍后再看/标签，并给出分级「更多类似」推荐。
// 纯只读页面，未启用持久化时进度与标签区自动降级隐藏。
func (s *Server) handleVideoDetail(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	md := s.library.Metadata(v.ID)
	hasMeta := md != nil
	if md == nil {
		md = &media.Metadata{}
	}

	data := videoDetailPageData{
		basePage: basePage{Title: v.Name, AuthEnabled: s.authEnabled()},
		Video:    v,
		Metadata: md,
		StoreOn:  s.storeEnabled(),
		HasMeta:  hasMeta,
	}

	// 剧集归属：提供「返回剧集」入口，同剧成员也是最高优先级推荐来源
	if sid, ok := s.library.ShowIDOf(v.ID); ok {
		data.ShowID = sid
		if sh, ok := s.library.GetShow(sid); ok {
			data.ShowTitle = sh.Title
		}
	}

	if s.storeEnabled() {
		state := s.loadVideoState()
		data.Fav = state.Favs[v.ID]
		data.WatchLater = state.WatchLater[v.ID]
		if tags, err := s.store.VideoTags(v.ID); err == nil {
			data.Tags = tags
		}
		if rt, err := s.store.GetRating(v.ID); err == nil && rt != nil {
			data.MyStars = rt.Stars
		}
		if p, ok := state.Progress[v.ID]; ok && hasProgressValue(p.Position, p.Duration) {
			data.Progress = detailProgress{
				Available: true,
				Position:  p.Position,
				Duration:  p.Duration,
				Percent:   progressPercentOf(p.Position, p.Duration),
				Remain:    remainOf(p.Position, p.Duration),
			}
		}
	}

	data.Similar = s.buildSimilarVideos(v, md)
	s.render(w, "video.html", data)
}

// buildSimilarVideos 分级产出「更多类似」推荐，排除自身：
//  1. 同剧集其他成员（剧集场景，理由「同剧集」）
//  2. 与刮削元信息有共同类型的视频（按共同类型数排序，理由「共同类型：xx」）
//  3. 与用户标签有共同标签的视频（持久化开启时，理由「共同标签：xx」）
//  4. 同目录伙伴兜底（零元信息视频也能有推荐，理由「同目录」）
//
// 已被更高级别收录的视频不重复出现；总数封顶 similarSize。
func (s *Server) buildSimilarVideos(v *media.Video, md *media.Metadata) []detailCard {
	seen := map[string]bool{v.ID: true}
	cards := make([]detailCard, 0, similarSize)

	// 级别 1：同剧集成员，按媒体库既有顺序（文件名排序，与剧集页一致）
	if showID, ok := s.library.ShowIDOf(v.ID); ok {
		memberSet := s.library.ShowVideos(showID)
		for _, other := range s.library.List() {
			if len(cards) >= similarSize {
				return cards
			}
			if !memberSet[other.ID] || seen[other.ID] {
				continue
			}
			seen[other.ID] = true
			cards = append(cards, detailCard{
				Video: other, Metadata: s.library.Metadata(other.ID), Reason: "同剧集",
			})
		}
	}

	// 级别 2：共同类型。统计每个候选的命中类型数，按命中数倒序、同序按文件名稳定排序
	if len(md.Genres) > 0 {
		selfGenres := make(map[string]bool, len(md.Genres))
		for _, g := range md.Genres {
			if g = strings.TrimSpace(g); g != "" {
				selfGenres[g] = true
			}
		}
		type genreHit struct {
			card   detailCard
			weight int
			order  int
		}
		var hits []genreHit
		for order, other := range s.library.List() {
			if seen[other.ID] {
				continue
			}
			omd := s.library.Metadata(other.ID)
			if omd == nil {
				continue
			}
			var shared []string
			for _, g := range omd.Genres {
				if selfGenres[strings.TrimSpace(g)] {
					shared = append(shared, strings.TrimSpace(g))
				}
			}
			if len(shared) == 0 {
				continue
			}
			hits = append(hits, genreHit{
				card: detailCard{
					Video: other, Metadata: omd,
					Reason: "共同类型：" + strings.Join(shared, "、"),
				},
				weight: len(shared),
				order:  order,
			})
		}
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].weight != hits[j].weight {
				return hits[i].weight > hits[j].weight
			}
			return hits[i].order < hits[j].order
		})
		for _, h := range hits {
			if len(cards) >= similarSize {
				return cards
			}
			seen[h.card.Video.ID] = true
			cards = append(cards, h.card)
		}
	}

	// 级别 3：共同用户标签（仅持久化开启）。取当前视频标签名，扫描其他视频的标签交集
	if s.storeEnabled() {
		selfTags, err := s.store.VideoTags(v.ID)
		if err == nil && len(selfTags) > 0 {
			selfTagNames := make(map[string]bool, len(selfTags))
			for _, t := range selfTags {
				selfTagNames[t.Name] = true
			}
			for _, other := range s.library.List() {
				if len(cards) >= similarSize {
					return cards
				}
				if seen[other.ID] {
					continue
				}
				otags, err := s.store.VideoTags(other.ID)
				if err != nil {
					continue
				}
				var shared []string
				for _, t := range otags {
					if selfTagNames[t.Name] {
						shared = append(shared, t.Name)
					}
				}
				if len(shared) == 0 {
					continue
				}
				seen[other.ID] = true
				cards = append(cards, detailCard{
					Video: other, Metadata: s.library.Metadata(other.ID),
					Reason: "共同标签：" + strings.Join(shared, "、"),
				})
			}
		}
	}

	// 级别 4：同目录兜底，复用播放页同目录相关视频口径，保证零元信息视频也有推荐
	dir := videoDir(v.RelPath)
	if dir != "" {
		for _, other := range s.library.List() {
			if len(cards) >= similarSize {
				break
			}
			if seen[other.ID] || videoDir(other.RelPath) != dir {
				continue
			}
			seen[other.ID] = true
			cards = append(cards, detailCard{
				Video: other, Metadata: s.library.Metadata(other.ID), Reason: "同目录",
			})
		}
	}
	return cards
}

// remainOf 计算剩余秒数，时长未知返回 0（模板侧据此显示「总时长未知」）。
func remainOf(position, duration float64) float64 {
	if duration <= 0 {
		return 0
	}
	r := duration - position
	if r < 0 {
		return 0
	}
	return r
}
