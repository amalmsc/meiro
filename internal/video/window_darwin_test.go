//go:build darwin && !ios

package video

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCaptionMenuAndVideoReplacement(t *testing.T) {
	a := &window{ctx: context.Background()}
	tt := ui.NewTester(a.view, 800, 500)
	tt.Frame()
	if err := tt.Click("CC: Loading…"); err != nil {
		t.Fatal(err)
	}
	if tt.Menu() != nil {
		t.Fatal("captions can be selected before loading")
	}
	a.captionsReady = true
	tt.Frame()
	if err := tt.Click("CC: Unavailable"); err != nil {
		t.Fatal(err)
	}
	if tt.Menu() != nil {
		t.Fatal("a video without captions offers a caption menu")
	}
	// Metadata contains uploaded captions, duplicate automatic originals,
	// an automatic translation, non-VTT formats and a live-chat pseudo-track.
	var resolved resolvedVideo
	if err := json.Unmarshal([]byte(`{
		"subtitles": {
			"en": [{"ext":"json3","url":"https://example.test/ignored"},{"ext":"vtt","name":"English","url":"https://example.test/en"}],
			"live_chat": [{"ext":"vtt","name":"Chat","url":"https://example.test/chat"}]
		},
		"automatic_captions": {
			"en": [{"ext":"vtt","name":"English","url":"https://example.test/auto-en"}],
			"ja": [{"ext":"vtt","name":"Japanese","url":"https://example.test/ja"}],
			"ja-orig": [{"ext":"vtt","name":"Japanese (Original)","url":"https://example.test/ja-orig"}],
			"fr": [{"ext":"vtt","name":"French","url":"https://example.test/fr"}]
		}
	}`), &resolved); err != nil {
		t.Fatal(err)
	}
	a.captionChoices = resolved.captions()
	tt.Frame()
	if err := tt.Click("CC: Off"); err != nil {
		t.Fatal(err)
	}
	want := []string{"Off", "English · en", "French (automatic) · fr", "Japanese (Original) (automatic) · ja-orig"}
	if got := tt.Menu(); !slices.Equal(got, want) {
		t.Fatalf("caption menu = %v, want %v", got, want)
	}
	tt.CloseMenu()
	a.captionTracks = []captionTrack{{ID: 7, Type: "sub", Title: "English · en", Selected: true}}
	tt.Frame()
	if _, ok := tt.Find("CC: On"); !ok {
		t.Fatal("selected captions do not show CC: On")
	}
	if !a.Load(Playback{URL: "https://www.youtube.com/watch?v=another"}) {
		t.Fatal("cannot replace video")
	}
	tt.Frame()
	if err := tt.Click("CC: Loading…"); err != nil {
		t.Fatal(err)
	}
	if tt.Menu() != nil {
		t.Fatal("new video offers the previous video's captions")
	}
	// Loading a video without captions must not revive the old catalogue.
	a.captionsReady = true
	tt.Frame()
	if _, ok := tt.Find("CC: Unavailable"); !ok {
		t.Fatal("previous video's captions survived replacement")
	}
}
