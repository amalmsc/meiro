package main

import (
	"context"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/m3"
	"github.com/elianiva/meiro/youtube"
)

// Video browsing has its own signed-out WEB client and search state. Neither
// music credentials nor music search filters are sent to regular YouTube.
type videoBrowserState struct {
	client                *youtube.Client
	query, request, topic string
	items                 []youtube.VideoItem
	more, err, moreErr    string
	loading               bool
	load, next            task
	grid                  ui.GridState
}

var videoTopics = []struct{ label, query string }{
	{"Discover", "popular videos"},
	{"Science", "science documentary"},
	{"Technology", "technology"},
	{"Gaming", "gaming"},
	{"Travel", "travel documentary"},
	{"Cooking", "cooking"},
}

func (a *app) openVideoBrowser() {
	if a.videos.client == nil {
		a.videos.client = youtube.NewClient(youtube.Options{BaseURL: "https://www.youtube.com"})
	}
	if a.videos.request == "" {
		a.browseVideoTopic(videoTopics[0].label, videoTopics[0].query)
	}
}

func (a *app) browseVideoTopic(label, query string) {
	a.videos.query = ""
	a.findVideos(query, label)
}

func (a *app) findVideos(query, topic string) {
	query = strings.TrimSpace(query)
	if query == "" {
		a.browseVideoTopic(videoTopics[0].label, videoTopics[0].query)
		return
	}
	s := &a.videos
	s.next.stop()
	s.request, s.topic = query, topic
	s.items, s.more, s.err, s.moreErr, s.loading = nil, "", "", "", true
	s.grid.ScrollTo(0, ui.Start)
	client := s.client
	startTask(a, &s.load, searchTimeout, func(ctx context.Context) (*youtube.VideoResult, error) {
		return client.SearchVideos(ctx, query)
	}, func(result *youtube.VideoResult, err error) {
		s.loading = false
		if err != nil {
			s.err, _ = failure(err)
			if errors.Is(err, youtube.ErrUnreachable) {
				s.err = "Could not reach YouTube. Check your connection and retry."
			}
			return
		}
		s.items, s.more = result.Items, result.ContinuationToken
	})
}

func (a *app) moreVideos() {
	s := &a.videos
	if s.loading || s.next.busy() || s.more == "" {
		return
	}
	token, client := s.more, s.client
	s.moreErr = ""
	startTask(a, &s.next, searchTimeout, func(ctx context.Context) (*youtube.VideoResult, error) {
		return client.ContinueVideos(ctx, token)
	}, func(result *youtube.VideoResult, err error) {
		if err != nil {
			s.moreErr, _ = failure(err)
			if errors.Is(err, youtube.ErrUnreachable) {
				s.moreErr = "Could not reach YouTube. Check your connection and retry."
			}
			return
		}
		seen := make(map[string]bool, len(s.items))
		for _, item := range s.items {
			seen[item.ID] = true
		}
		for _, item := range result.Items {
			if !seen[item.ID] {
				s.items = append(s.items, item)
				seen[item.ID] = true
			}
		}
		s.more = result.ContinuationToken
	})
}

func (a *app) videoSearchBar(c *ui.Context) {
	result, _ := m3.SearchBar(c, m3.SearchSpec{
		Value: &a.videos.query, Placeholder: "Search YouTube videos", Key: "video-search",
		Height: 48, MaxWidth: 520, Container: searchTint(c),
	})
	if result.Submitted {
		a.findVideos(a.videos.query, "")
	} else if result.Cleared {
		a.browseVideoTopic(videoTopics[0].label, videoTopics[0].query)
	}
}

func (a *app) videoPage(c *ui.Context) {
	s, sc := &a.videos, m3.Of(c).Scheme
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Wrap().Padding(8, pageGutter).Gap(8).AlignItems(ui.Center).Children(func() {
			for _, topic := range videoTopics {
				if m3.Chip(c, topic.label, s.topic == topic.label, "video-topic-"+topic.label).Clicked() {
					a.browseVideoTopic(topic.label, topic.query)
				}
			}
			if m3.IconButton(c, m3.IconButtonSpec{Icon: m3.IconRefresh, Label: "Refresh videos", Key: "refresh-videos"}).Clicked() {
				a.findVideos(s.request, s.topic)
			}
		})
		m3.Text(c, m3.BodyMedium, "Browse topics or search all of YouTube. Click a video to watch.").TextColor(sc.OnSurfaceVariant).Padding(0, pageGutter, 12)
		switch {
		case s.loading:
			ui.Column(c).Grow(1).FillWidth().Center().Gap(12).Children(func() {
				m3.LoadingIndicator(c, 48, false)
				m3.Text(c, m3.BodyMedium, "Finding videos…")
			})
		case s.err != "":
			a.message(c, m3.IconError, "Could not load videos", s.err, "Retry", func() { a.findVideos(s.request, s.topic) })
		case len(s.items) == 0:
			a.message(c, m3.IconSearch, "No videos found", "Try another search or browse a topic.", "", nil)
		default:
			items := s.items
			s.grid.Key = func(i int) any { return items[i].ID }
			s.grid.Label = func(i int) string { return items[i].Title }
			ui.GridView(c, &s.grid, len(items), 260, 292, func(i int) {
				a.videoCard(c, items[i])
			}).Grow(1).Gap(16).Padding(0, pageGutter, 8)
			if s.more != "" {
				ui.Row(c).Justify(ui.Center).AlignItems(ui.Center).Gap(12).Padding(8).Children(func() {
					if s.moreErr != "" {
						m3.Text(c, m3.BodySmall, s.moreErr).TextColor(sc.Error).MaxLines(2)
					}
					label := "More videos"
					if s.moreErr != "" {
						label = "Retry more videos"
					}
					if m3.Button(c, m3.ButtonSpec{Label: label, Kind: m3.Tonal, Disabled: s.next.busy(), Key: "more-videos"}).Clicked() {
						a.moreVideos()
					}
				})
			}
		}
		ui.Box(c).Height(a.clearance()).Shrink(0)
	})
}

func (a *app) videoCard(c *ui.Context, item youtube.VideoItem) {
	sc := m3.Of(c).Scheme
	card := ui.ButtonBase(c.Key("watch-" + item.ID))
	card.Column().AlignItems(ui.Start).Justify(ui.Start).Fill().Padding(8).Gap(8).Radius(m3.Large).Cursor(ui.CursorPointer).Label("Watch " + item.Title).
		Background(m3.StateFill(sc.SurfaceContainerLow, sc.OnSurface, card.Hovered(), card.Pressed(), card.FocusVisible()))
	card.Children(func() {
		ui.Box(c).FillWidth().Height(164).Shrink(0).Radius(m3.Medium).Background(sc.SurfaceContainerHighest).Children(func() {
			if bitmap := a.thumbs.Bitmap(item.Thumbnail, a.artPixels(640, 320)); bitmap != nil {
				ui.Image(c, bitmap).Fill().Fit(ui.Cover).Radius(m3.Medium)
			} else {
				ui.Box(c).Fill().Center().Children(func() { ui.Icon(c, m3.IconPlay).FontSize(36).TextColor(sc.OnSurfaceVariant) })
			}
			if item.Duration != "" {
				m3.Text(c, m3.LabelSmall, item.Duration).Absolute().Right(8).Bottom(8).Padding(3, 6).Radius(m3.ExtraSmall).Background(ui.RGB(0, 0, 0).Alpha(0.8)).TextColor(ui.RGB(255, 255, 255))
			}
		})
		m3.EmphasizedText(c, m3.TitleSmall, item.Title).MaxLines(2).FillWidth().TextColor(sc.OnSurface)
		m3.Text(c, m3.BodySmall, item.Channel).SingleLine().FillWidth().TextColor(sc.OnSurfaceVariant)
		if item.Views != "" {
			m3.Text(c, m3.BodySmall, item.Views).SingleLine().FillWidth().TextColor(sc.OnSurfaceVariant)
		}
	})
	if card.Clicked() {
		if err := a.playVideo("https://www.youtube.com/watch?v=" + item.ID); err != nil {
			a.notice = err.Error()
		}
	}
}
