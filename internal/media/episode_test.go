package media

import "testing"

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		name        string
		stem        string
		wantOK      bool
		wantSeason  int
		wantEpisode int
		wantEnd     int
		wantTitle   string
	}{
		{
			name:        "标准 SxxEyy 点分",
			stem:        "Show.Name.S01E02.1080p.BluRay",
			wantOK:      true,
			wantSeason:  1,
			wantEpisode: 2,
			wantTitle:   "Show Name",
		},
		{
			name:        "短标记 S1E2",
			stem:        "Show S1E2",
			wantOK:      true,
			wantSeason:  1,
			wantEpisode: 2,
			wantTitle:   "Show",
		},
		{
			name:        "括号包裹的标记",
			stem:        "剧 [S02E05] 合集",
			wantOK:      true,
			wantSeason:  2,
			wantEpisode: 5,
			wantTitle:   "剧 合集",
		},
		{
			name:        "多集 S01E02E03",
			stem:        "Show.S01E02E03",
			wantOK:      true,
			wantSeason:  1,
			wantEpisode: 2,
			wantEnd:     3,
			wantTitle:   "Show",
		},
		{
			name:        "x 记法 1x02",
			stem:        "Show.1x02.WEB-DL",
			wantOK:      true,
			wantSeason:  1,
			wantEpisode: 2,
			wantTitle:   "Show",
		},
		{
			name:        "中文第x季第x集",
			stem:        "庆余年 第1季第2集",
			wantOK:      true,
			wantSeason:  1,
			wantEpisode: 2,
			wantTitle:   "庆余年",
		},
		{
			name:        "中文数字季集",
			stem:        "琅琊榜 第二季第十三集",
			wantOK:      true,
			wantSeason:  2,
			wantEpisode: 13,
			wantTitle:   "琅琊榜",
		},
		{
			name:        "仅中文集号，季兜底1",
			stem:        "动漫 第三集",
			wantOK:      true,
			wantSeason:  1,
			wantEpisode: 3,
			wantTitle:   "动漫",
		},
		{
			name:        "仅 EP 标记，季兜底1",
			stem:        "Show.EP08.2160p",
			wantOK:      true,
			wantSeason:  1,
			wantEpisode: 8,
			wantTitle:   "Show",
		},
		{
			name:      "电影反例：年份分辨率无标记",
			stem:      "Movie 2024 1080p BluRay",
			wantOK:    false,
			wantTitle: "Movie",
		},
		{
			name:      "电影反例：纯中文名",
			stem:      "肖申克的救赎",
			wantOK:    false,
			wantTitle: "肖申克的救赎",
		},
		{
			name:      "电影反例：编码噪声不构成集号",
			stem:      "Film.2023.2160p.x265.AAC",
			wantOK:    false,
			wantTitle: "Film",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseEpisode(tc.stem)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v, info=%+v", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got.Season != tc.wantSeason || got.Episode != tc.wantEpisode || got.EpisodeEnd != tc.wantEnd {
				t.Errorf("季集不匹配: got=(S%d E%d-%d), want=(S%d E%d-%d)",
					got.Season, got.Episode, got.EpisodeEnd, tc.wantSeason, tc.wantEpisode, tc.wantEnd)
			}
			if got.Title != tc.wantTitle {
				t.Errorf("剧名规范化: got=%q, want=%q", got.Title, tc.wantTitle)
			}
		})
	}
}

func TestNormalizeTitle(t *testing.T) {
	cases := map[string]string{
		"Show.Name.S01E02.1080p.BluRay.x264": "Show Name",
		"剧A_第1季第3集_4K_WEB-DL":                "剧A",
		"[字幕组] Show S02E01 [10bit HEVC]":     "Show",
		"Movie.2024.1080p.BluRay":            "Movie",
		"  Multiple   Spaces__--  S01E01 ":   "Multiple Spaces",
	}
	for in, want := range cases {
		if got := normalizeTitle(in); got != want {
			t.Errorf("normalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCNNum(t *testing.T) {
	cases := map[string]int{
		"3": 3, "十": 10, "二十": 20, "十三": 13,
		"一百": 100, "一百零一": 101, "二十三": 23, "九十九": 99,
	}
	for in, want := range cases {
		if got := cnNum(in); got != want {
			t.Errorf("cnNum(%q) = %d, want %d", in, got, want)
		}
	}
}
