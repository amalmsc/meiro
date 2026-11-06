//go:build darwin && !ios

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/internal/history"
	"github.com/elianiva/meiro/internal/playstate"
	"github.com/elianiva/meiro/internal/video"
	"github.com/elianiva/meiro/youtube"
)

// The video backend is native-only. Its stand-in exposes the same user actions;
// music still decodes real audio, through ffmpeg and the null output.
type testVideoWindow struct {
	paused, closed  bool
	input           string
	onPlay, onClose func()
	onProgress      func(video.Playback)
	state           video.Playback
}

func (v *testVideoWindow) Load(input video.Playback) bool {
	v.input, v.paused, v.state = input.URL, input.Paused, input
	return !v.closed
}

func (v *testVideoWindow) emit() {
	if v.onProgress != nil {
		v.state.Paused = v.paused
		v.onProgress(v.state)
	}
}

func (v *testVideoWindow) Pause()   { v.paused = true; v.emit() }
func (v *testVideoWindow) Focus()   {}
func (v *testVideoWindow) Wait()    {}
func (v *testVideoWindow) Release() {}
func (v *testVideoWindow) Close() {
	v.emit()
	v.closed = true
	v.onClose()
}

func (v *testVideoWindow) view(c *ui.Context) {
	if v.closed {
		ui.Text(c, "Video closed")
		return
	}
	if v.paused {
		ui.Text(c, "Video paused")
	} else {
		ui.Text(c, "Video playing")
	}
	ui.Button(c, "Play video").OnClick(func() { v.onPlay(); v.paused = false; v.emit() })
	if v.onProgress != nil {
		ui.Button(c, "Start video playback").OnClick(func() {
			v.state.URL, v.state.Title, v.state.Channel = v.input, "A video title", "Example channel"
			v.state.Started, v.state.Duration = true, 600*time.Second
			v.state.Position = max(v.state.Position, time.Second)
			v.emit()
		})
		ui.Button(c, "Watch 1s").OnClick(func() { v.state.Position += time.Second; v.emit() })
		ui.Button(c, "Pause video").OnClick(v.Pause)
		ui.Button(c, "Forward 10s").OnClick(func() { v.state.Position += 10 * time.Second; v.state.SeekVersion++; v.emit() })
		ui.Button(c, "Back 10s").OnClick(func() { v.state.Position = max(0, v.state.Position-10*time.Second); v.state.SeekVersion++; v.emit() })
		ui.Button(c, "End video").OnClick(func() { v.state.Position = v.state.Duration; v.state.Ended = true; v.emit() })
	}
	ui.Button(c, "Close video").OnClick(v.Close)
}

func TestVideoAndMusicTakeTurns(t *testing.T) {
	for _, loading := range []bool{false, true} {
		name := "playing music"
		if loading {
			name = "music still resolving"
		}
		t.Run(name, func(t *testing.T) {
			a, _ := tonesApp(t, 600)
			var v *testVideoWindow
			a.openVideo = func(_ video.Playback, play func(), _ func(video.Playback), close func()) (video.Window, error) {
				v = &testVideoWindow{onPlay: play, onClose: close}
				return v, nil
			}
			a.router.Push("/album/MPREb_test")
			tt := ui.NewTester(a.view, 1000, 700)
			tt.Frame()
			click := func(label string) {
				t.Helper()
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
			}
			var pending []func()
			inline := a.run
			if loading {
				a.run = func(work func()) { pending = append(pending, work) }
			}
			click("Album Track One")
			a.run = inline
			if !loading {
				pollUntil(t, a, "music playback", func() bool { return a.player.Position() > 100*time.Millisecond })
			}
			click("Account")
			click("Open YouTube URL")
			tt.Type("https://youtu.be/aqz-KE-bpKQ")
			click("Watch video")
			for _, work := range pending {
				work()
			}
			if !a.player.Paused() {
				t.Fatal("music plays over the video")
			}
			if loading && len(a.history) != 0 {
				t.Fatal("unheard music was recorded as played")
			}
			vt := ui.NewTester(v.view, 600, 600)
			vt.Frame()
			if !vt.HasText("Video playing") {
				t.Fatal("video did not start")
			}
			// Space in the music window is the normal play/pause shortcut.
			tt.Key(0, ui.KeySpace)
			tt.Frame()
			vt.Frame()
			if a.player.Paused() || !vt.HasText("Video paused") {
				t.Fatal("playing music did not pause the video")
			}
			if len(a.history) != 1 {
				t.Fatalf("music should be recorded once when heard: %+v", a.history)
			}
			if err := vt.Click("Play video"); err != nil {
				t.Fatal(err)
			}
			if !a.player.Paused() {
				t.Fatal("resuming video did not pause music")
			}
			if err := vt.Click("Close video"); err != nil {
				t.Fatal(err)
			}
			if !a.player.Paused() {
				t.Fatal("closing video unexpectedly resumed music")
			}
			tt.Key(0, ui.KeySpace)
			tt.Frame()
			if a.player.Paused() {
				t.Fatal("music could not resume after closing video")
			}
		})
	}
}

func TestInvalidVideoLinkLeavesMusicPlaying(t *testing.T) {
	a, _ := tonesApp(t, 600)
	a.router.Push("/album/MPREb_test")
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	for _, label := range []string{"Album Track One", "Account", "Open YouTube URL"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	tt.Type("https://youtube.com.evil.example/watch?v=aqz-KE-bpKQ")
	if err := tt.Click("Watch video"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("paste an HTTPS YouTube video link") || a.player.Paused() {
		t.Fatalf("invalid link interrupted music or omitted validation: %v", tt.Texts())
	}
}

// Regular YouTube fixtures are separate from fakeMusic, so accidentally using
// music search cannot produce the expected video cards.
func videoBrowserApp(t *testing.T) (*app, *bool) {
	t.Helper()
	a := newTestApp(t)
	// Player tests start from a public search, not an account's home feed.
	a.videos.request = "popular videos"
	failMore := false
	a.videos.client = youtube.NewClient(youtube.Options{HTTPClient: &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		var body struct{ Query, Continuation string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		if r.URL.Path != "/youtubei/v1/search" || r.Header.Get("X-Youtube-Client-Name") != "1" {
			t.Errorf("not regular YouTube search: %s %v", r.URL, r.Header)
		}
		if body.Query == "offline" || body.Continuation != "" && failMore {
			return nil, errors.New("offline")
		}
		items := []any{}
		token := "next"
		add := func(id, title string) {
			items = append(items, map[string]any{"videoRenderer": map[string]any{
				"videoId": id, "title": map[string]string{"simpleText": title},
				"ownerText":     map[string]string{"simpleText": "Example channel"},
				"lengthText":    map[string]string{"simpleText": "12:34"},
				"viewCountText": map[string]string{"simpleText": "1,234 views"},
			}})
		}
		switch {
		case body.Query == "empty":
			token = ""
		case body.Continuation != "":
			add("aqz-KE-bpKQ", "Duplicate video")
			add("jNQXAC9IVRw", "Next video")
			token = ""
		default:
			add("aqz-KE-bpKQ", body.Query+" video")
		}
		if token != "" {
			items = append(items, map[string]any{"continuationItemRenderer": map[string]any{"continuationEndpoint": map[string]any{"continuationCommand": map[string]string{"token": token}}}})
		}
		var response any = map[string]any{"contents": map[string]any{"twoColumnSearchResultsRenderer": map[string]any{"primaryContents": map[string]any{"sectionListRenderer": map[string]any{"contents": items}}}}}
		if body.Continuation != "" {
			response = map[string]any{"onResponseReceivedCommands": []any{map[string]any{"appendContinuationItemsAction": map[string]any{"continuationItems": items}}}}
		}
		encoded, err := json.Marshal(response)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, err
	})}})
	return a, &failMore
}

func TestBrowseSearchAndWatchVideos(t *testing.T) {
	a, failMore := videoBrowserApp(t)
	musicQuery := a.search.query
	var v *testVideoWindow
	opens := 0
	a.openVideo = func(input video.Playback, play func(), _ func(video.Playback), close func()) (video.Window, error) {
		opens++
		v = &testVideoWindow{input: input.URL, state: input, paused: input.Paused, onPlay: play, onClose: close}
		return v, nil
	}
	a.router.Push("/youtube")
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if !tt.HasText("popular videos video") {
		t.Fatal(tt.Texts())
	}
	click("Search YouTube videos")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("golang")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !tt.HasText("golang video") || a.search.query != musicQuery {
		t.Fatal("video search changed music or omitted results")
	}
	click("Watch golang video")
	if v.input != "https://www.youtube.com/watch?v=aqz-KE-bpKQ" || !a.watchingVideo {
		t.Fatal("card did not start video")
	}
	*failMore = true
	click("More videos")
	if !tt.HasText("golang video") || !tt.HasText("Retry more videos") {
		t.Fatal("pagination failure lost results", tt.Texts())
	}
	*failMore = false
	click("Retry more videos")
	if len(a.videos.items) != 2 || a.videos.items[0].Title != "golang video" {
		t.Fatal("pagination did not append/dedupe")
	}
	click("Watch Next video")
	if opens != 1 || v.input != "https://www.youtube.com/watch?v=jNQXAC9IVRw" {
		t.Fatal("next card did not reuse player")
	}
	for _, query := range []string{"empty", "offline"} {
		click("Search YouTube videos")
		tt.Key(ui.Cmd, ui.KeyA)
		tt.Type(query)
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		want := "No videos found"
		if query == "offline" {
			want = "Could not load videos"
		}
		if !tt.HasText(want) {
			t.Fatal(tt.Texts())
		}
	}
	click("For you")
	if !tt.HasText("Sign in for recommendations") {
		t.Fatal(tt.Texts())
	}
}

func TestVideoHistoryStartsWithPlayback(t *testing.T) {
	for _, mode := range []string{"signed out", "signed in", "report rejected"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := videoBrowserApp(t)
			a.historyPath = filepath.Join(t.TempDir(), "history.json")
			var reports []string
			auth, err := youtube.NewCookieAuth("SAPISID=test", youtube.CookieOptions{})
			if err != nil {
				t.Fatal(err)
			}
			client := youtube.NewClient(youtube.Options{CookieAuth: auth, HTTPClient: &http.Client{Transport: playbackRoundTrip(func(r *http.Request) (*http.Response, error) {
				body, status := "", http.StatusOK
				switch r.URL.Path {
				case "/youtubei/v1/player":
					var request struct{ VideoID string }
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						return nil, err
					}
					body = `{"playbackTracking":{"videostatsPlaybackUrl":{"baseUrl":"https://s.youtube.com/api/stats/playback?docid=` + request.VideoID + `"},"videostatsWatchtimeUrl":{"baseUrl":"https://s.youtube.com/api/stats/watchtime"}}}`
				case "/api/stats/playback":
					reports = append(reports, r.URL.Query().Get("docid"))
					if !strings.Contains(r.Header.Get("Cookie"), "SAPISID=test") {
						t.Error("history report omitted the signed-in session")
					}
					if mode == "report rejected" {
						status = http.StatusForbidden
					}
				case "/api/stats/watchtime":
					body = ""
				default:
					return (fakeMusic{}).RoundTrip(r)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}})
			if mode != "signed out" {
				a.account.session = &session{client: client}
			}
			var v *testVideoWindow
			a.openVideo = func(input video.Playback, play func(), progress func(video.Playback), close func()) (video.Window, error) {
				v = &testVideoWindow{input: input.URL, state: input, paused: input.Paused, onPlay: play, onProgress: progress, onClose: close}
				return v, nil
			}
			a.router.Push("/youtube")
			tt := ui.NewTester(a.view, 1000, 700)
			tt.Frame()
			click := func(tt *ui.Tester, label string) {
				t.Helper()
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
			}
			click(tt, "Watch popular videos video")
			vt := ui.NewTester(v.view, 600, 600)
			vt.Frame()
			click(vt, "Close video") // loading/failed/cancelled playback is not a play
			if len(a.history) != 0 || len(reports) != 0 {
				t.Fatal("recorded a video that never started")
			}
			click(tt, "Account")
			click(tt, "Open YouTube URL")
			tt.Type("https://youtu.be/aqz-KE-bpKQ")
			click(tt, "Watch video")
			vt = ui.NewTester(v.view, 600, 600)
			vt.Frame()
			click(vt, "Start video playback")
			click(vt, "Play video") // resume does not count as another play
			if len(a.history) != 1 {
				t.Fatalf("first video history = %+v", a.history)
			}
			click(tt, "More videos")
			click(tt, "Watch Next video")
			if len(a.history) != 1 {
				t.Fatal("recorded the replacement before playback started")
			}
			click(vt, "Start video playback")
			a.historyWriter.Wait()
			entries := history.Read(a.historyPath)
			wantIDs := []string{"aqz-KE-bpKQ", "jNQXAC9IVRw"}
			if len(entries) != 2 {
				t.Fatalf("persisted video history = %+v", entries)
			}
			for i, entry := range entries {
				if entry.VideoID != wantIDs[i] || entry.Title != "A video title" || entry.Artist != "Example channel" || entry.PlayedAt.IsZero() {
					t.Fatalf("persisted video %d = %+v", i, entry)
				}
			}
			if mode == "signed out" {
				wantIDs = nil
			}
			if !slices.Equal(reports, wantIDs) {
				t.Fatalf("account reports=%v, want %v", reports, wantIDs)
			}
		})
	}
}

func TestVideoResumesPausedAfterRestart(t *testing.T) {
	for _, withMusic := range []bool{false, true} {
		t.Run(fmt.Sprint("music queue ", withMusic), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "playback.json")
			if withMusic {
				w := playstate.Writer{Path: path}
				w.Save(playstate.State{Items: []youtube.MusicItem{{VideoID: "song", Title: "Saved song", Duration: "3:00"}}, Position: 17 * time.Second})
				w.Wait()
			}
			first, _ := videoBrowserApp(t)
			first.restorePlayback(path)
			var v *testVideoWindow
			first.openVideo = func(input video.Playback, play func(), progress func(video.Playback), close func()) (video.Window, error) {
				v = &testVideoWindow{input: input.URL, state: input, paused: input.Paused, onPlay: play, onProgress: progress, onClose: close}
				return v, nil
			}
			first.router.Push("/youtube")
			tt := ui.NewTester(first.view, 1000, 700)
			tt.Frame()
			if err := tt.Click("Watch popular videos video"); err != nil {
				t.Fatal(err)
			}
			vt := ui.NewTester(v.view, 600, 600)
			vt.Frame()
			for _, label := range []string{"Start video playback", "Forward 10s", "Forward 10s", "Forward 10s", "Watch 1s", "Pause video"} {
				if err := vt.Click(label); err != nil {
					t.Fatal(err)
				}
				vt.Frame()
			}
			first.playSave.writer.Wait()
			if saved := playstate.Load(path).Video; saved == nil || saved.Position != 32*time.Second || !saved.Open || saved.Title != "A video title" {
				t.Fatalf("paused video was not persisted: %+v", saved)
			}
			first.shutdown()
			second, _ := videoBrowserApp(t)
			second.openVideo = func(input video.Playback, play func(), progress func(video.Playback), close func()) (video.Window, error) {
				v = &testVideoWindow{input: input.URL, state: input, paused: input.Paused, onPlay: play, onProgress: progress, onClose: close}
				return v, nil
			}
			second.restorePlayback(path)
			second.restoreVideo()
			if v.closed || !v.paused || v.state.Position != 32*time.Second || v.state.Started || len(second.history) != 0 {
				t.Fatalf("restart did not restore silently paused: %+v, history=%v", v, second.history)
			}
			if withMusic && (second.current.VideoID != "song" || second.playbackPosition() != 17*time.Second || second.player.Active()) {
				t.Fatal("video resume replaced the saved music queue or started audio")
			}
			vt = ui.NewTester(v.view, 600, 600)
			vt.Frame()
			for _, label := range []string{"Play video", "Start video playback", "Close video"} {
				if err := vt.Click(label); err != nil {
					t.Fatal(err)
				}
				vt.Frame()
			}
			second.playSave.writer.Wait()
			third, _ := videoBrowserApp(t)
			third.openVideo = second.openVideo
			third.restorePlayback(path)
			third.restoreVideo()
			if third.videoWindow != nil || third.videoReopen {
				t.Fatal("restart reopened an explicitly closed video")
			}
		})
	}
}

func TestVideoAccountProgressExcludesSeeks(t *testing.T) {
	a, _ := videoBrowserApp(t)
	var tracking []*http.Request
	auth, err := youtube.NewCookieAuth("SAPISID=test", youtube.CookieOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a.account.session = &session{client: youtube.NewClient(youtube.Options{CookieAuth: auth, HTTPClient: &http.Client{Transport: playbackRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"playerConfig":{"playbackStartConfig":{"startSeconds":45}},"playbackTracking":{"videostatsPlaybackUrl":{"baseUrl":"https://s.youtube.com/api/stats/playback"},"videostatsWatchtimeUrl":{"baseUrl":"https://s.youtube.com/api/stats/watchtime"}}}`
		if r.Method == http.MethodGet {
			tracking = append(tracking, r)
			body = ""
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}})}
	var v *testVideoWindow
	a.openVideo = func(input video.Playback, play func(), progress func(video.Playback), close func()) (video.Window, error) {
		v = &testVideoWindow{input: input.URL, state: input, paused: input.Paused, onPlay: play, onProgress: progress, onClose: close}
		return v, nil
	}
	a.router.Push("/youtube")
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if err := tt.Click("Watch popular videos video"); err != nil {
		t.Fatal(err)
	}
	if v.state.Position != 45*time.Second || len(tracking) != 0 {
		t.Fatalf("account resume = %v, reports=%d", v.state, len(tracking))
	}
	vt := ui.NewTester(v.view, 600, 600)
	vt.Frame()
	click := func(label string) {
		t.Helper()
		if err := vt.Click(label); err != nil {
			t.Fatal(err)
		}
		vt.Frame()
	}
	click("Start video playback")
	for range 19 {
		click("Watch 1s")
	}
	// Leave the periodic report in flight while the user seeks and pauses.
	// Its older acknowledgement must not put the skipped range back.
	inline := a.run
	var pending []func()
	a.run = func(work func()) { pending = append(pending, work) }
	click("Watch 1s")
	click("Forward 10s")
	click("Watch 1s")
	click("Pause video")
	a.run = inline
	for _, work := range pending {
		work()
	}
	click("Play video")
	click("Watch 1s")
	click("Back 10s")
	click("Watch 1s")
	click("Close video")
	want := []struct{ path, from, to, final string }{
		{"/api/stats/playback", "", "45.000", ""},
		{"/api/stats/watchtime", "45.000", "65.000", ""},
		{"/api/stats/watchtime", "75.000", "76.000", "1"},
		{"/api/stats/playback", "", "76.000", ""},
		{"/api/stats/watchtime", "67.000", "68.000", "1"},
	}
	if len(tracking) != len(want) {
		t.Fatalf("tracking count=%d, want %d: %v", len(tracking), len(want), tracking)
	}
	for i, r := range tracking {
		q := r.URL.Query()
		if r.URL.Path != want[i].path || q.Get("st") != want[i].from || q.Get("cmt") != want[i].to || q.Get("final") != want[i].final || q.Get("c") != "WEB" {
			t.Errorf("tracking %d: %s, want %+v", i, r.URL, want[i])
		}
	}
	if len(a.history) != 1 {
		t.Fatalf("resume or seek duplicated history: %v", a.history)
	}
}

func TestVideoResumeLocalPreferenceAndAccountFailure(t *testing.T) {
	for _, tc := range []struct {
		name            string
		local, want     time.Duration
		failed, pending bool
	}{
		{name: "local wins", local: 37 * time.Second, want: 37 * time.Second},
		{name: "completed local starts over", local: 599 * time.Second},
		{name: "account unavailable", failed: true},
		{name: "sign out during account lookup", pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := videoBrowserApp(t)
			if tc.local > 0 {
				w := playstate.Writer{Path: filepath.Join(t.TempDir(), "playback.json")}
				w.Save(playstate.State{Video: &playstate.Video{URL: "https://www.youtube.com/watch?v=aqz-KE-bpKQ", Position: tc.local, Duration: 600 * time.Second}})
				w.Wait()
				a.restorePlayback(w.Path)
			}
			auth, err := youtube.NewCookieAuth("SAPISID=test", youtube.CookieOptions{})
			if err != nil {
				t.Fatal(err)
			}
			lookups := 0
			a.account.session = &session{client: youtube.NewClient(youtube.Options{CookieAuth: auth, HTTPClient: &http.Client{Transport: playbackRoundTrip(func(r *http.Request) (*http.Response, error) {
				lookups++
				status := http.StatusOK
				if tc.failed {
					status = http.StatusServiceUnavailable
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"playerConfig":{"playbackStartConfig":{"startSeconds":89}}}`)), Request: r}, nil
			})}})}
			var v *testVideoWindow
			a.openVideo = func(input video.Playback, play func(), progress func(video.Playback), close func()) (video.Window, error) {
				v = &testVideoWindow{input: input.URL, state: input, paused: input.Paused, onPlay: play, onProgress: progress, onClose: close}
				return v, nil
			}
			a.router.Push("/youtube")
			tt := ui.NewTester(a.view, 1000, 700)
			tt.Frame()
			var pending []func()
			inline := a.run
			if tc.pending {
				a.run = func(work func()) { pending = append(pending, work) }
			}
			if err := tt.Click("Watch popular videos video"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if tc.pending {
				for _, label := range []string{"Account", "Sign out"} {
					if err := tt.Click(label); err != nil {
						t.Fatal(err)
					}
					tt.Frame()
				}
				a.run = inline
				for _, work := range pending {
					work()
				}
				if v != nil {
					t.Fatal("stale account lookup opened a video after sign-out")
				}
				return
			}
			if v == nil || v.state.Position != tc.want {
				t.Fatalf("resume=%+v, want %s", v, tc.want)
			}
			if tc.local > 0 && lookups != 0 {
				t.Fatal("account lookup overrode local progress")
			}
		})
	}
}

func TestVideoSearchDiscardsSupersededResults(t *testing.T) {
	a, _ := videoBrowserApp(t)
	var work []func()
	a.run = func(fn func()) { work = append(work, fn) }
	a.router.Push("/youtube")
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if err := tt.Click("Search YouTube videos"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("gaming")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	// The latest response arrives first; the previous search must not replace it.
	for i := len(work) - 1; i >= 0; i-- {
		work[i]()
	}
	tt.Frame()
	if !tt.HasText("gaming video") || tt.HasText("popular videos video") {
		t.Fatal(tt.Texts())
	}
}

func TestVideoRecommendationsFollowSelectedProfile(t *testing.T) {
	resetFakes()
	a := signedInApp(t)
	a.account.session.channels = []youtube.AccountChannel{
		{Name: "Personal", Selected: true},
		{Name: "Brand channel", ChannelID: "UC-brand"},
	}
	searchApp, _ := videoBrowserApp(t)
	a.videos.client = searchApp.videos.client
	tt := open(a, "/youtube")
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	mustShow(t, tt, "For you", "Personal recommendation")
	for _, label := range []string{"Discover", "Science", "Technology", "Gaming", "Travel", "Cooking"} {
		if tt.HasText(label) {
			t.Fatalf("removed topic still visible: %s", label)
		}
	}
	click("More videos")
	if len(a.videos.items) != 2 {
		t.Fatalf("home pagination lost or duplicated results: %+v", a.videos.items)
	}
	mustShow(t, tt, "Personal next recommendation")
	click("Account")
	click("Brand channel")
	mustShow(t, tt, "UC-brand recommendation")
	if tt.HasText("Personal recommendation") || tt.HasText("Personal next recommendation") {
		t.Fatal("switching profile kept the previous profile's feed")
	}
	click("Search YouTube videos")
	tt.Type("golang")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	mustShow(t, tt, "golang video")
	click("For you")
	mustShow(t, tt, "UC-brand recommendation")
	if a.videos.query != "" {
		t.Fatal("returning home retained the search text")
	}
	// Hold a continuation across sign-out: its late result must not restore
	// private recommendations or its old pagination token.
	inline := a.run
	var pending []func()
	a.run = func(work func()) { pending = append(pending, work) }
	click("More videos")
	click("Account")
	click("Sign out")
	a.run = inline
	for _, work := range pending {
		work()
	}
	tt.Frame()
	mustShow(t, tt, "Sign in for recommendations")
	if len(a.videos.items) != 0 || a.videos.more != "" || tt.HasText("UC-brand next recommendation") {
		t.Fatal("signed-out page exposed the previous profile's recommendations")
	}
	click("Search YouTube videos")
	tt.Type("public")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	mustShow(t, tt, "public video")
}

func TestVideoHomeRetryAndStaleProfileResults(t *testing.T) {
	resetFakes()
	s := &switchable{mode: "offline"}
	a := stateApp(t, s, true)
	tt := open(a, "/youtube")
	mustShow(t, tt, "Could not load videos")
	s.mode = ""
	if err := tt.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	mustShow(t, tt, "Personal recommendation")
	// A restored/switching account may finish while the previous home request
	// is in flight. Deliver that old response after the new profile's feed.
	inline := a.run
	var pending []func()
	a.run = func(work func()) { pending = append(pending, work) }
	if err := tt.Click("Refresh videos"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.run = inline
	auth, err := youtube.NewCookieAuth("SAPISID=test", youtube.CookieOptions{AccountIndex: 1, OnBehalfOfUser: "UC-other"})
	if err != nil {
		t.Fatal(err)
	}
	a.useSession(&session{client: a.account.newClient(auth), cookie: "SAPISID=test", channel: "UC-other", accountIndex: 1}, sessionSource{})
	for _, work := range pending {
		work()
	}
	tt.Frame()
	mustShow(t, tt, "UC-other recommendation")
	if tt.HasText("Personal recommendation") {
		t.Fatal("old home response replaced the current profile's feed")
	}
}
