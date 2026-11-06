package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
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
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Goog-Authuser") != "" {
			t.Error("anonymous search sent account credentials")
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

const videoHomeFirstPage = `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
 {"tabRenderer":{"title":"Other","content":{"richGridRenderer":{"contents":[{"videoRenderer":{"videoId":"other-tab"}},{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"other-tab-token"}}}}]}}}},
 {"tabRenderer":{"title":"Home","selected":true,"content":{"richGridRenderer":{
  "header":{"chipCloudRenderer":{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"chip"}}}}},
  "contents":[
   {"richItemRenderer":{"content":{"videoRenderer":{"videoId":"z-home","title":{"simpleText":"Home video"},"shortBylineText":{"simpleText":"Home channel"}}}}},
   {"richItemRenderer":{"content":{"adSlotRenderer":{"videoRenderer":{"videoId":"ad"}}}}},
   {"richSectionRenderer":{"content":{"richShelfRenderer":{"isShorts":true,"contents":[{"videoRenderer":{"videoId":"short-shelf"}}]}}}},
   {"richShelfRenderer":{"isShorts":true,"contents":[{"videoRenderer":{"videoId":"short"}}]}},
   {"richItemRenderer":{"content":{"reelItemRenderer":{"videoId":"reel"}}}},
   {"richItemRenderer":{"content":{"shortsLockupViewModel":{"entityId":"shorts-lockup"}}}},
   {"richItemRenderer":{"content":{"videoRenderer":{"videoId":"short-endpoint","navigationEndpoint":{"reelWatchEndpoint":{}}}}}},
   {"richItemRenderer":{"content":{"videoRenderer":{"videoId":"short-page","navigationEndpoint":{"commandMetadata":{"webCommandMetadata":{"webPageType":"WEB_PAGE_TYPE_SHORTS"}}}}}}},
   {"richItemRenderer":{"content":{"playlistRenderer":{"playlistId":"playlist","videos":[{"videoRenderer":{"videoId":"preview"}}]}}}},
   {"richItemRenderer":{"content":{"lockupViewModel":{"contentId":"playlist-lockup","contentType":"LOCKUP_CONTENT_TYPE_PLAYLIST"}}}},
   {"richItemRenderer":{"content":{"lockupViewModel":{"contentId":"a-home","contentType":"LOCKUP_CONTENT_TYPE_VIDEO","metadata":{"lockupMetadataViewModel":{"title":{"content":"Modern home video"}}}}}}},
   {"richItemRenderer":{"content":{"videoRenderer":{"videoId":"z-home","title":{"simpleText":"Duplicate"}}}}},
   {"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"home-next"}}}}
  ]
 }}}}
],"secondaryContents":{"videoRenderer":{"videoId":"sidebar"}}}}}`

const videoHomeNextPage = `{"onResponseReceivedActions":[
 {"reloadContinuationItemsCommand":{"targetId":"feed-filter-chips","continuationItems":[{"videoRenderer":{"videoId":"chip-video"}},{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"chip-next"}}}}]}},
 {"appendContinuationItemsAction":{"targetId":"browse-feedFEwhat_to_watch","continuationItems":[
  {"richItemRenderer":{"content":{"videoRenderer":{"videoId":"b-next","title":{"simpleText":"Next home video"}}}}},
  {"richItemRenderer":{"content":{"lockupViewModel":{"contentId":"b-next","contentType":"LOCKUP_CONTENT_TYPE_VIDEO"}}}},
  {"richItemRenderer":{"content":{"videoRenderer":{"videoId":"a-home"}}}},
  {"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"home-last"}}}}
 ]}}
]}`

func TestVideoHomeAndContinueIdentity(t *testing.T) {
	auth, err := NewCookieAuth("SAPISID=home-secret", CookieOptions{AccountIndex: 2, OnBehalfOfUser: "UCselected"})
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"", defaultAPIURL, "https://www.youtube.com", "local"} {
		t.Run(base, func(t *testing.T) {
			calls := 0
			origin := "https://www.youtube.com"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/youtubei/v1/browse" || r.URL.Query().Get("key") != "home-key" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Origin") != origin || r.Header.Get("Referer") != origin+"/" {
					t.Errorf("origin/referer = %q / %q", r.Header.Get("Origin"), r.Header.Get("Referer"))
				}
				if r.Header.Get("Cookie") != "SAPISID=home-secret" || r.Header.Get("X-Goog-Authuser") != "2" || r.Header.Get("X-Goog-PageId") != "UCselected" || r.Header.Get("X-Goog-Visitor-Id") != "" {
					t.Errorf("selected identity not retained: %v", r.Header)
				}
				sent := r.Header.Get("Authorization")
				timestamp, err := strconv.ParseInt(strings.SplitN(strings.TrimPrefix(sent, "SAPISIDHASH "), "_", 2)[0], 10, 64)
				if err != nil || sent != "SAPISIDHASH "+sapisidHash("home-secret", origin, timestamp) {
					t.Error("authorization was not hashed for the request origin")
				}
				if r.Header.Get("X-Youtube-Client-Name") != "1" || r.Header.Get("X-Youtube-Client-Version") != "home-web" {
					t.Errorf("not WEB headers: %v", r.Header)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				context := payload["context"].(map[string]any)
				client := context["client"].(map[string]any)
				if client["clientName"] != "WEB" || client["clientVersion"] != "home-web" || client["hl"] != "fr" || client["gl"] != "CA" || client["visitorData"] != nil || context["user"].(map[string]any)["onBehalfOfUser"] != "UCselected" {
					t.Errorf("context = %v", context)
				}
				if len(payload) != 2 || payload["isAudioOnly"] != nil {
					t.Errorf("unexpected payload fields: %v", payload)
				}
				if calls == 1 {
					if payload["browseId"] != "FEwhat_to_watch" {
						t.Errorf("home payload = %v", payload)
					}
					_, _ = w.Write([]byte(videoHomeFirstPage))
				} else {
					if payload["continuation"] != "home-next" {
						t.Errorf("continuation payload = %v", payload)
					}
					_, _ = w.Write([]byte(videoHomeNextPage))
				}
			}))
			defer server.Close()
			endpoint, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if base == "local" {
				base, origin = server.URL, server.URL
			}
			// Route every request to the local server, even when testing the
			// production host selection. No external network is used.
			httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme+"://"+r.URL.Host != origin {
					t.Errorf("request host = %s, want %s", r.URL, origin)
				}
				local := r.Clone(r.Context())
				local.URL.Scheme, local.URL.Host = endpoint.Scheme, endpoint.Host
				return server.Client().Transport.RoundTrip(local)
			})}
			client := NewClient(Options{BaseURL: base, HTTPClient: httpClient, APIKey: "home-key", WebClientVersion: "home-web", Language: "fr", Country: "CA", VisitorData: "anonymous", CookieAuth: auth, AllowInsecureCookieAuth: base == server.URL})
			originalBase := client.baseURL
			first, err := client.GetVideoHome(context.Background())
			want := &VideoResult{Items: []VideoItem{{ID: "z-home", Title: "Home video", Channel: "Home channel"}, {ID: "a-home", Title: "Modern home video"}}, ContinuationToken: "home-next"}
			if err != nil || !reflect.DeepEqual(first, want) {
				t.Fatalf("home = %#v, %v; want %#v", first, err, want)
			}
			next, err := client.ContinueVideoHome(context.Background(), first.ContinuationToken)
			want = &VideoResult{Items: []VideoItem{{ID: "b-next", Title: "Next home video"}, {ID: "a-home"}}, ContinuationToken: "home-last"}
			if err != nil || !reflect.DeepEqual(next, want) {
				t.Fatalf("next = %#v, %v; want %#v", next, err, want)
			}
			if calls != 2 || client.baseURL != originalBase || client.cookieAuth != auth {
				t.Errorf("calls = %d, or original client mutated", calls)
			}
		})
	}
}

func TestVideoHomeRequiresSignInAndToken(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	client := NewClient(Options{BaseURL: server.URL})
	if result, err := client.GetVideoHome(context.Background()); result != nil || err == nil || !strings.Contains(err.Error(), "sign-in") {
		t.Errorf("signed-out home = %#v, %v", result, err)
	}
	if result, err := client.ContinueVideoHome(context.Background(), "next"); result != nil || err == nil || !strings.Contains(err.Error(), "sign-in") {
		t.Errorf("signed-out continuation = %#v, %v", result, err)
	}
	auth, err := NewCookieAuth("SAPISID=secret", CookieOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client = NewClient(Options{BaseURL: server.URL, CookieAuth: auth, AllowInsecureCookieAuth: true})
	for _, token := range []string{"", " \t\n"} {
		if result, err := client.ContinueVideoHome(context.Background(), token); result != nil || err == nil {
			t.Errorf("blank continuation = %#v, %v", result, err)
		}
	}
	if calls != 0 {
		t.Errorf("invalid input made %d requests", calls)
	}
}

func TestVideoHomeErrorsAndEmptyPages(t *testing.T) {
	auth, err := NewCookieAuth("SAPISID=secret", CookieOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"HTTP failure", "unavailable", http.StatusServiceUnavailable, true},
		{"rejected session", "unauthorized", http.StatusUnauthorized, true},
		{"API error", `{"error":{"code":400,"message":"bad request"}}`, http.StatusOK, true},
		{"invalid JSON", `{`, http.StatusOK, true},
		{"null", `null`, http.StatusOK, true},
		{"array", `[]`, http.StatusOK, true},
		{"missing feed", `{}`, http.StatusOK, true},
		{"search response", videosFirstPage, http.StatusOK, true},
		{"wrong continuation target", `{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"targetId":"feed-filter-chips","continuationItems":[]}}]}`, http.StatusOK, true},
		{"malformed grid", `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"richGridRenderer":{"contents":{}}}}}]}}}`, http.StatusOK, true},
		{"empty", ``, http.StatusOK, false},
	} {
		for _, continuation := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/continuation=%t", test.name, continuation), func(t *testing.T) {
				body := test.body
				if test.name == "empty" {
					body = `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"richGridRenderer":{"contents":[]}}}}]}}}`
					if continuation {
						body = `{"onResponseReceivedEndpoints":[{"reloadContinuationItemsCommand":{"targetId":"browse-feedFEwhat_to_watch","continuationItems":[]}}]}`
					}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(body))
				}))
				defer server.Close()
				client := NewClient(Options{BaseURL: server.URL, CookieAuth: auth, AllowInsecureCookieAuth: true})
				var result *VideoResult
				var err error
				if continuation {
					result, err = client.ContinueVideoHome(context.Background(), "next")
				} else {
					result, err = client.GetVideoHome(context.Background())
				}
				if test.wantError {
					if err == nil || result != nil {
						t.Fatalf("response = %#v, %v; want error", result, err)
					}
					if test.status == http.StatusUnauthorized && !errors.Is(err, ErrUnauthorized) {
						t.Errorf("unauthorized error = %v", err)
					}
				} else if err != nil || !reflect.DeepEqual(result, &VideoResult{Items: []VideoItem{}}) {
					t.Errorf("empty response = %#v, %v", result, err)
				}
			})
		}
	}
}
