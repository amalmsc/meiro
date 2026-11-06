package youtube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Reduced WEB search shapes: mixed renderer generations, an ad with a nested
// video, and filter continuations both before and inside the result section.
const videosFirstPage = `{
 "header":{"chipCloudRenderer":{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"header-chip"}}}}},
 "contents":{"twoColumnSearchResultsRenderer":{"primaryContents":{"sectionListRenderer":{
  "header":{"chipCloudRenderer":{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"section-chip"}}}}},
  "contents":[{"itemSectionRenderer":{"contents":[
   {"adSlotRenderer":{"fulfillmentContent":{"videoRenderer":{"videoId":"advertisement"}}}},
   {"channelRenderer":{"channelId":"UCchannel"}},
   {"playlistRenderer":{"playlistId":"PLlist","videos":[{"videoRenderer":{"videoId":"playlist-preview"}}]}},
   {"lockupViewModel":{"contentId":"PLlockup","contentType":"LOCKUP_CONTENT_TYPE_PLAYLIST"}},
   {"lockupViewModel":{
    "contentId":"z-first","contentType":"LOCKUP_CONTENT_TYPE_VIDEO",
    "contentImage":{"thumbnailViewModel":{"image":{"sources":[{"url":"https://img.test/z-small"},{"url":"https://img.test/z"}]},"overlays":[{"thumbnailOverlayBadgeViewModel":{"thumbnailBadges":[{"thumbnailBadgeViewModel":{"text":"12:34"}}]}}]}},
    "metadata":{"lockupMetadataViewModel":{"title":{"content":"First video"},"metadata":{"contentMetadataViewModel":{"metadataRows":[
     {"metadataParts":[{"text":{"content":"First channel","commandRuns":[{"onTap":{"innertubeCommand":{"browseEndpoint":{"browseId":"UCfirst"}}}}]}}]},
     {"metadataParts":[{"text":{"content":"1.2K views"}},{"text":{"content":"2 days ago"}}]}
    ]}}}}
   }},
   {"videoRenderer":{"videoId":"a-second","title":{"runs":[{"text":"Second "},{"text":"video"}]},"longBylineText":{"runs":[{"text":"Second channel"}]},"thumbnail":{"thumbnails":[{"url":"https://img.test/a"}]},"lengthText":{"simpleText":"3:24:59"},"viewCountText":{"simpleText":"2,520,400 views"}}},
   {"videoRenderer":{"videoId":"z-first","title":{"simpleText":"Duplicate"}}},
   {"videoRenderer":{"title":{"simpleText":"No ID"}}}
  ]}},
  {"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"videos-next","request":"CONTINUATION_REQUEST_TYPE_SEARCH"}}}}
 ]}},"secondaryContents":{"videoRenderer":{"videoId":"sidebar"}}}}
}`

const videosNextPage = `{"onResponseReceivedCommands":[
 {"appendContinuationItemsAction":{"targetId":"search-filter-chips","continuationItems":[{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"wrong-next"}}}}]}},
 {"appendContinuationItemsAction":{"targetId":"search-feed","continuationItems":[
  {"itemSectionRenderer":{"contents":[
   {"videoRenderer":{"videoId":"y-third","title":{"simpleText":"Third video"},"ownerText":{"simpleText":"Third channel"},"shortViewCountText":{"simpleText":"42 views"},"thumbnailOverlays":[{"thumbnailOverlayTimeStatusRenderer":{"text":{"simpleText":"4:05"},"style":"DEFAULT"}}]}},
   {"videoRenderer":{"videoId":"b-live","title":{"simpleText":"Live video"}}},
   {"lockupViewModel":{"contentId":"y-third","contentType":"LOCKUP_CONTENT_TYPE_VIDEO"}}
  ]}},
  {"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"videos-last"}}}}
 ]}}
]}`

func TestSearchVideosAndContinue(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/youtubei/v1/search" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("X-Youtube-Client-Name") != "1" || r.Header.Get("X-Youtube-Client-Version") != "web-test-version" {
			t.Errorf("not WEB headers: %v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		client := payload["context"].(map[string]any)["client"].(map[string]any)
		if client["clientName"] != "WEB" || client["clientVersion"] != "web-test-version" {
			t.Errorf("not WEB context: %v", client)
		}
		if _, ok := payload["isAudioOnly"]; ok {
			t.Error("regular videos asked for audio only")
		}
		if calls == 1 {
			if payload["query"] != "  Go tutorial  " || payload["params"] != "EgIQAQ%3D%3D" || payload["continuation"] != nil {
				t.Errorf("search payload = %v", payload)
			}
			_, _ = w.Write([]byte(videosFirstPage))
		} else {
			if payload["continuation"] != "videos-next" || payload["query"] != nil || payload["params"] != nil {
				t.Errorf("continuation payload = %v", payload)
			}
			_, _ = w.Write([]byte(videosNextPage))
		}
	}))
	defer server.Close()
	client := NewClient(Options{BaseURL: server.URL, WebClientVersion: "web-test-version"})
	first, err := client.SearchVideos(context.Background(), "  Go tutorial  ")
	if err != nil {
		t.Fatal(err)
	}
	wantFirst := &VideoResult{Items: []VideoItem{
		{ID: "z-first", Title: "First video", Channel: "First channel", Thumbnail: "https://img.test/z", Duration: "12:34", Views: "1.2K views"},
		{ID: "a-second", Title: "Second video", Channel: "Second channel", Thumbnail: "https://img.test/a", Duration: "3:24:59", Views: "2,520,400 views"},
	}, ContinuationToken: "videos-next"}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("first = %#v, want %#v", first, wantFirst)
	}
	next, err := client.ContinueVideos(context.Background(), first.ContinuationToken)
	if err != nil {
		t.Fatal(err)
	}
	wantNext := &VideoResult{Items: []VideoItem{
		{ID: "y-third", Title: "Third video", Channel: "Third channel", Duration: "4:05", Views: "42 views"},
		{ID: "b-live", Title: "Live video"},
	}, ContinuationToken: "videos-last"}
	if !reflect.DeepEqual(next, wantNext) {
		t.Fatalf("next = %#v, want %#v", next, wantNext)
	}
	if calls != 2 {
		t.Errorf("requests = %d, want 2", calls)
	}
}

func TestVideosEmptyInputAndErrors(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := NewClient(Options{BaseURL: server.URL})
	for _, input := range []string{"", " \n\t"} {
		if _, err := client.SearchVideos(context.Background(), input); err == nil {
			t.Error("empty query accepted")
		}
		if _, err := client.ContinueVideos(context.Background(), input); err == nil {
			t.Error("empty token accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("empty input made %d requests", calls)
	}
	if _, err := client.SearchVideos(context.Background(), "go"); err == nil {
		t.Error("search swallowed HTTP error")
	}
	if _, err := client.ContinueVideos(context.Background(), "next"); err == nil {
		t.Error("continuation swallowed HTTP error")
	}
}

func TestVideosEmptyResults(t *testing.T) {
	for _, body := range []string{`{}`, `{"contents":{"twoColumnSearchResultsRenderer":{"primaryContents":{"sectionListRenderer":{"contents":[]}}}}}`} {
		client := searchServer(t, body, nil)
		result, err := client.SearchVideos(context.Background(), "nothing")
		if err != nil || result == nil || len(result.Items) != 0 || result.ContinuationToken != "" {
			t.Errorf("empty response = %#v, %v", result, err)
		}
	}
}
