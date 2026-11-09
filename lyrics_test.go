package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/internal/betterlyrics"
	"github.com/elianiva/meiro/internal/credits"
	"github.com/elianiva/meiro/internal/lrclib"
	"github.com/elianiva/meiro/internal/lyrics"
	"github.com/elianiva/meiro/internal/playstate"
	"github.com/elianiva/meiro/m3"
	"github.com/elianiva/meiro/youtube"
)

func TestBetterLyricsComesBeforeYouTubeMusicAndLRCLIB(t *testing.T) {
	a := newTestApp(t)
	a.betterLyrics = func(context.Context, betterlyrics.Query) ([]lyrics.Line, error) {
		return []lyrics.Line{
			{At: 0, Text: "first words", Words: []lyrics.Word{{At: 0, Text: "first "}, {At: time.Second, Text: "words"}}},
			{At: 30 * time.Second, Text: "second words", Words: []lyrics.Word{{At: 30 * time.Second, Text: "second "}, {At: 31 * time.Second, Text: "words"}}},
		}, nil
	}
	asked := 0
	a.lyricsSource = func(context.Context, lrclib.Query) (*lrclib.Lyrics, error) {
		asked++
		return &lrclib.Lyrics{Synced: []lyrics.Line{{At: 0, Text: "from LRCLIB"}}}, nil
	}
	a.queue.restore(playstate.State{Items: tracks("a"), Index: 0, RecStart: -1})
	a.current = a.queue.current()
	tt := ui.NewTester(a.miniPlayer, miniWidth, miniHeight)
	a.setMiniShown(true)
	tt.Frame()
	if err := tt.Click("Lyrics"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("first words") || tt.HasText("from LRCLIB") {
		t.Fatalf("the lyrics are %q", tt.Texts())
	}
	if asked != 0 {
		t.Fatalf("LRCLIB was asked %d times with lyrics already found", asked)
	}
	if err := tt.Click("Where these lyrics come from"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Better Lyrics") {
		t.Fatalf("the source is not named: %q", tt.Texts())
	}
}

func TestLyricsAreLookedUpAgainOnceTheLengthIsKnown(t *testing.T) {
	a := newTestApp(t)
	var asked []time.Duration
	a.betterLyrics = func(_ context.Context, q betterlyrics.Query) ([]lyrics.Line, error) {
		asked = append(asked, q.Duration)
		if q.Duration == 0 {
			// Better Lyrics keys its cache on the length, and serves only
			// cached songs: without one, it has nothing.
			return nil, nil
		}
		return []lyrics.Line{{At: 0, Text: "word timed", Words: []lyrics.Word{{At: 0, Text: "word timed"}}}}, nil
	}
	a.queue.restore(playstate.State{Items: tracks("a"), Index: 0, RecStart: -1})
	a.current = a.queue.current()
	a.total = 0
	a.loadLyrics()
	if len(asked) != 1 || asked[0] != 0 || a.nowPlaying.lyrics.footer != "Source: YouTube Music" {
		t.Fatalf("the first lookup asked for %v and found %q", asked, a.nowPlaying.lyrics.footer)
	}
	// The stream resolves, and its length goes with the lyrics lookups.
	a.total = 212 * time.Second
	a.refreshLyrics()
	if len(asked) != 2 || asked[1] != 212*time.Second {
		t.Fatalf("the second lookup asked for %v", asked)
	}
	if len(a.nowPlaying.lyrics.synced) != 1 || a.nowPlaying.lyrics.footer != "Source: Better Lyrics" {
		t.Fatalf("the lyrics are %#v from %q", a.nowPlaying.lyrics.synced, a.nowPlaying.lyrics.footer)
	}
	// One lookup with the length is all a track gets.
	a.refreshLyrics()
	a.loadLyrics()
	if len(asked) != 2 {
		t.Fatalf("the lyrics were asked for %v", asked)
	}
}

func TestLyricsUseTheArtistKnownForTheTrack(t *testing.T) {
	a := newTestApp(t)
	var asked betterlyrics.Query
	a.betterLyrics = func(_ context.Context, q betterlyrics.Query) ([]lyrics.Line, error) {
		asked = q
		return nil, nil
	}
	// An album row leaves the artist out; another list named it before.
	a.queue.restore(playstate.State{Items: []youtube.MusicItem{{VideoID: "song", Title: "E85", Kind: youtube.KindTrack, Duration: "2:34"}}, Index: 0, RecStart: -1})
	a.current = a.queue.current()
	a.scrobble.credits.Put("song", credits.Credits{Artist: "Don Toliver", Album: "OCTANE"})
	a.total = 154 * time.Second
	a.loadLyrics()
	if asked.Artist != "Don Toliver" || asked.Album != "OCTANE" || asked.Duration != 154*time.Second {
		t.Fatalf("the lyrics were looked up as %+v", asked)
	}
}

func TestTheSungLineSitsInTheMiddleOfTheLyrics(t *testing.T) {
	a := newTestApp(t)
	lines := make([]lyrics.Line, 10)
	for i := range lines {
		lines[i] = lyrics.Line{At: time.Duration(i) * 6 * time.Second, Text: fmt.Sprintf("line %d", i)}
	}
	a.lyricsSource = func(context.Context, lrclib.Query) (*lrclib.Lyrics, error) {
		return &lrclib.Lyrics{Synced: lines}, nil
	}
	a.queue.restore(playstate.State{Items: tracks("a"), Index: 0, RecStart: -1})
	a.current = a.queue.current()
	// Twenty-four seconds into a four-minute song: the fifth of the ten lines.
	a.total, a.resumeID, a.scrub = 4*time.Minute, a.current.VideoID, 24
	a.nowPlaying.tab = tabLyrics
	tt := ui.NewTester(a.miniPlayer, miniWidth, miniHeight)
	a.setMiniShown(true)
	for range 4 {
		tt.Frame()
	}
	sung, ok := tt.Find(lines[4].Text)
	if !ok {
		t.Fatalf("the line being sung is not shown: %q", tt.Texts())
	}
	// The lyrics fill the panel between the modes above them and the buttons
	// below them, so their middle is where the sung line belongs.
	modes, ok := tt.Find("Synced lyrics")
	if !ok {
		t.Fatalf("the lyrics have no switch: %q", tt.Texts())
	}
	footer, ok := tt.Find("Open Meiro")
	if !ok {
		t.Fatalf("the mini player has no buttons: %q", tt.Texts())
	}
	middle := (modes.Y + modes.H + footer.Y) / 2
	if got := sung.Y + sung.H/2; got < middle-32 || got > middle+32 {
		t.Fatalf("the sung line is at %.0f, not in the middle of the lyrics at %.0f", got, middle)
	}
}

func TestRomanizationAndTranslationStayUnderTheirOwnLines(t *testing.T) {
	a := newTestApp(t)
	a.current = tracks("a")[0]
	a.total, a.resumeID, a.scrub = time.Minute, "a", 12
	a.betterLyrics = func(context.Context, betterlyrics.Query) ([]lyrics.Line, error) {
		return []lyrics.Line{{At: 0, Text: "真夜中", Romanization: "mayonaka"}, {At: 10 * time.Second, Text: "笑顔", Romanization: "egao"}}, nil
	}
	asked := false
	a.lyricTranslation = func(context.Context, string, []string) ([]betterlyrics.Translation, error) {
		asked = true
		return []betterlyrics.Translation{{Text: "midnight"}, {Text: "smile"}}, nil
	}
	a.openFloatingLyrics()
	tt := ui.NewTester(a.floatingLyricsView, 480, 480)
	for range 4 {
		tt.Frame()
	}
	if !tt.HasText("笑顔") || tt.HasText("egao") || tt.HasText("smile") {
		t.Fatalf("readings should stay hidden until asked for: %q", tt.Texts())
	}
	if asked {
		t.Fatal("translation was requested before it was turned on")
	}
	if err := tt.Click("Romanization"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !asked || !tt.HasText("egao") || tt.HasText("smile") {
		t.Fatalf("romanization should load without the translation: asked=%t %q", asked, tt.Texts())
	}
	if err := tt.Click("Translation"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !asked || !tt.HasText("smile") || !tt.HasText("midnight") {
		t.Fatalf("translation did not load under both lines: asked=%t %q", asked, tt.Texts())
	}
	original, _ := tt.Find("笑顔")
	reading, _ := tt.Find("egao")
	translated, _ := tt.Find("smile")
	if reading.Y <= original.Y || translated.Y <= reading.Y {
		t.Fatalf("readings are not below their lyric: original=%v roman=%v translation=%v", original, reading, translated)
	}
}

func TestLyricWordsLightAsTheyAreSung(t *testing.T) {
	sc := m3.New(m3.Config{}, true).Scheme
	line := lyrics.Line{At: 0, Text: "one two three", Words: []lyrics.Word{
		{At: 0, Text: "one "},
		{At: time.Second, Text: "two "},
		{At: 2 * time.Second, Text: "three"},
	}}
	spans := lyricWords(line, time.Second, sc.OnSurface)
	if len(spans) != 3 || spans[0].Text != "one " || spans[1].Text != "two " || spans[2].Text != "three" {
		t.Fatalf("the words are %#v", spans)
	}
	if spans[0].Color != sc.OnSurface || spans[1].Color != sc.OnSurface {
		t.Fatalf("the words sung are not lit: %#v", spans)
	}
	if spans[2].Color == sc.OnSurface {
		t.Fatalf("the word still to come is lit: %#v", spans)
	}
}

func TestFloatingLyricsFollowPlaybackIndependently(t *testing.T) {
	a := newTestApp(t)
	a.betterLyrics = func(_ context.Context, q betterlyrics.Query) ([]lyrics.Line, error) {
		return []lyrics.Line{
			{At: 0, Text: q.Title + " opening"},
			{At: 10 * time.Second, Text: q.Title + " chorus"},
			{At: 40 * time.Second, Text: q.Title + " ending"},
		}, nil
	}
	a.queue.restore(playstate.State{Items: tracks("a", "b"), Index: 0, RecStart: -1})
	a.current = a.queue.current()
	a.total, a.resumeID, a.scrub = time.Minute, "a", 15
	main := ui.NewTester(a.miniPlayer, miniWidth, miniHeight)
	a.setMiniShown(true)
	main.Frame()
	if err := main.Click("Lyrics"); err != nil {
		t.Fatal(err)
	}
	main.Frame()
	if err := main.Click("Open floating lyrics"); err != nil {
		t.Fatal(err)
	}
	main.Frame()
	floating := ui.NewTester(a.floatingLyricsView, 480, 420)
	for range 8 {
		main.Frame()
		floating.Frame()
	}
	if !a.nowPlaying.floatingOpen || !floating.HasText(a.current.Title+" chorus") {
		t.Fatalf("floating lyrics did not open: %q", floating.Texts())
	}
	// Different-sized windows must each center the current line, even when
	// the other window has already followed it.
	for _, tt := range []*ui.Tester{main, floating} {
		line, ok := tt.Find(a.current.Title + " chorus")
		if !ok {
			t.Fatal("current line is missing")
		}
		modes, ok := tt.Find("Synced lyrics")
		if !ok {
			t.Fatal("lyrics switch is missing")
		}
		bottom := float32(420)
		if tt == main {
			footer, _ := main.Find("Open Meiro")
			bottom = footer.Y
		}
		middle := (modes.Y + modes.H + bottom) / 2
		if got := line.Y + line.H/2; got < middle-24 || got > middle+24 {
			t.Fatalf("current line center is %.0f, want %.0f", got, middle)
		}
	}
	floating.SetSize(320, 320)
	for range 4 {
		floating.Frame()
	}
	line, _ := floating.Find(a.current.Title + " chorus")
	if line.Y < 180 || line.Y+line.H > 320 {
		t.Fatalf("shrinking the window clipped the current lyric: %+v", line)
	}
	floating.SetSize(480, 420)
	floating.Frame()
	before := a.seekVersion
	if err := floating.Click(a.current.Title + " chorus"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	if a.seekVersion != before {
		t.Fatalf("clicking a floating line sought to %s", a.seekTarget)
	}
	line, ok := floating.Find(a.current.Title + " chorus")
	if !ok {
		t.Fatal("floating lyric line is missing")
	}
	if floatingPointerHitsControl(a, line.X+line.W/2, line.Y+line.H/2) {
		t.Fatal("a floating lyric line keeps the pointer")
	}
	for _, label := range []string{"Synced lyrics", "Romanization", "Translation", "Where these lyrics come from"} {
		control, ok := floating.Find(label)
		if !ok {
			t.Fatalf("floating lyrics lack %q", label)
		}
		if !floatingPointerHitsControl(a, control.X+control.W/2, control.Y+control.H/2) {
			t.Fatalf("%s passes the pointer through", label)
		}
	}
	if err := main.Click(a.current.Title + " chorus"); err != nil {
		t.Fatal(err)
	}
	main.Frame()
	if a.seekTarget != 10*time.Second {
		t.Fatalf("clicking a line in the player requested %s", a.seekTarget)
	}
	if floating.HasText("Volume") || floating.HasText("Position") {
		t.Fatal("extra controls should start collapsed")
	}
	if err := floating.Click("Show player controls"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	for _, label := range []string{"Play", "Previous", "Next", "Volume", "Position", "Playback speed: 1×"} {
		if _, ok := floating.Find(label); !ok {
			t.Fatalf("expanded controls lack %q", label)
		}
	}
	position, _ := floating.Find("Position")
	x, y := position.X+position.W*0.75, position.Y+position.H/2
	version := a.seekVersion
	floating.Press(x, y)
	floating.Frame()
	main.Frame()
	if a.seekVersion != version {
		t.Fatal("another window's frame ended the floating seek drag")
	}
	floating.Release(x, y)
	floating.Frame()
	if a.seekVersion == version || a.seekTarget <= 30*time.Second {
		t.Fatalf("releasing the floating position did not seek: %s", a.seekTarget)
	}
	if err := floating.Click("Mute"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	if a.volume != 0 {
		t.Fatal("floating mute did not mute playback")
	}
	if err := floating.Click("Hide player controls"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	if floating.HasText("Volume") || floating.HasText("Position") {
		t.Fatal("extra controls did not collapse")
	}
	for _, label := range []string{"Play", "Previous", "Next"} {
		if _, ok := floating.Find(label); !ok {
			t.Fatalf("collapsed controls hid %q", label)
		}
	}
	// Lyrics must keep loading with neither the main nor tray panel open.
	a.setMiniShown(false)
	a.nowPlaying.tab = tabQueue
	if err := floating.Click("Next"); err != nil {
		t.Fatal(err)
	}
	main.Frame()
	floating.Frame()
	if a.current.VideoID != "b" || !floating.HasText(a.current.Title+" opening") {
		t.Fatalf("floating lyrics did not follow the next song: %q", floating.Texts())
	}
	if err := floating.Click("Synced lyrics"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	if !a.settings.StaticLyrics || !floating.HasText(a.nowPlaying.lyrics.text) {
		t.Fatalf("floating static lyrics are missing: %q", floating.Texts())
	}
	if err := floating.Click("Close floating lyrics"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	if a.nowPlaying.floatingOpen {
		t.Fatal("floating lyrics did not close")
	}
}

func TestFloatingPlayerIsAvailableAcrossPagesAndHasIndependentTabs(t *testing.T) {
	a := newTestApp(t)
	a.settings.AutoPlay = false
	a.queue.restore(playstate.State{Items: tracks("song-related", "b"), Index: 0, RecStart: -1})
	a.current = a.queue.current()
	main := ui.NewTester(a.view, 1000, 800)
	for _, expanded := range []bool{false, true} {
		a.settings.RailExpanded = expanded
		for _, path := range []string{"/home", "/explore", "/search", "/settings", "/stats", "/playlist/VLPL_songs"} {
			a.router.Push(path)
			main.Frame()
			if err := main.Click("Floating lyrics"); err != nil {
				t.Fatalf("expanded=%t, page=%s: %v", expanded, path, err)
			}
			main.Frame()
			if !a.nowPlaying.floatingOpen || a.router.Path() != path {
				t.Fatalf("floating action navigated away from %s", path)
			}
		}
	}
	floating := ui.NewTester(a.floatingLyricsView, 480, 480)
	floating.Frame()
	if err := floating.Click("Related"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	if !floating.HasText("Related Song") || !floating.HasText("Related Playlist") {
		t.Fatalf("floating Related did not load: %q", floating.Texts())
	}
	a.router.Push("/home")
	if err := floating.Click("Related Playlist"); err != nil {
		t.Fatal(err)
	}
	if a.path() != "/playlist/VLPL_songs" || a.current.VideoID != "song-related" {
		t.Fatal("a floating related playlist should open its page without changing playback")
	}
	a.setMiniShown(true)
	mini := ui.NewTester(a.miniPlayer, miniWidth, miniHeight)
	mini.Frame()
	if !mini.HasText("Clear queue") || mini.HasText("Related Song") {
		t.Fatal("changing the floating tab changed the mini player's tab")
	}
	if err := floating.Click("Up next"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	if !floating.HasText("Track b") {
		t.Fatalf("floating Up next lacks the queued song: %q", floating.Texts())
	}
	if err := floating.Click("Clear queue"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	mini.Frame()
	if a.queue.remaining() != 0 || floating.HasText("Track b") || mini.HasText("Track b") {
		t.Fatal("clearing the floating queue did not update shared playback")
	}
	if err := floating.Click("Lyrics"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	if !floating.HasText("No lyrics for this song.") {
		t.Fatalf("returning to floating lyrics failed: %q", floating.Texts())
	}
	if err := floating.Click("Related"); err != nil {
		t.Fatal(err)
	}
	floating.Frame()
	if err := floating.Click("Related Song"); err != nil {
		t.Fatal(err)
	}
	if a.current.VideoID != "rel-song" || a.queue.source != "Related" {
		t.Fatal("choosing a floating related song did not play it")
	}
}

func TestFloatingLyricsLeaveGlassVisibleWithoutFadingText(t *testing.T) {
	for _, static := range []bool{false, true} {
		t.Run(fmt.Sprintf("static=%t", static), func(t *testing.T) {
			a := newTestApp(t)
			a.current = tracks("a")[0]
			a.total, a.resumeID = time.Minute, "a"
			a.settings.StaticLyrics = static
			a.betterLyrics = func(context.Context, betterlyrics.Query) ([]lyrics.Line, error) {
				return []lyrics.Line{{At: 0, Text: "Keep these lyrics clear"}}, nil
			}
			a.openFloatingLyrics()
			tt := ui.NewTester(a.floatingLyricsView, 480, 420)
			for _, glass := range []bool{true, false, true} {
				tt.SetVibrancy(glass)
				for range 4 {
					tt.Frame()
				}
				img := tt.Image()
				// Include both ends of the synced list: opaque fade overlays
				// must not cover the native material either.
				for _, y := range []int{4, 130, 400} {
					_, _, _, alpha := img.At(4, y).RGBA()
					if glass && alpha != 0 || !glass && alpha != 0xffff {
						t.Fatalf("glass=%t: background alpha at y=%d is %d", glass, y, alpha)
					}
				}
				line, ok := tt.Find("Keep these lyrics clear")
				if !ok {
					t.Fatal("lyrics are missing")
				}
				opaque := false
				for y := int(line.Y); y < int(line.Y+line.H); y++ {
					for x := int(line.X); x < int(line.X+line.W); x++ {
						_, _, _, alpha := img.At(x, y).RGBA()
						opaque = opaque || alpha == 0xffff
					}
				}
				if !opaque {
					t.Fatal("the current lyric was faded with its background")
				}
			}
		})
	}
}

// floatingPointerHitsControl reports whether the floating window keeps a
// click at x, y, in the window's own coordinates. Lyric text must not; the
// switches and buttons above it must.
func floatingPointerHitsControl(a *app, x, y float32) bool {
	for _, bounds := range a.nowPlaying.floatingHit {
		if expandHit(bounds, 8).Contains(x, y) {
			return true
		}
	}
	return false
}
