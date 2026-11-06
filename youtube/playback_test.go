package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseLoudnessPrefersPerceptualWhenIntegratedLoudnessIsZero(t *testing.T) {
	raw := json.RawMessage(`{"playerConfig":{"audioConfig":{"loudnessDb":0,"perceptualLoudnessDb":-12.5}}}`)
	got, err := parseLoudness(raw)
	if err != nil || got.LoudnessDB != -12.5 {
		t.Fatalf("parseLoudness() = %#v, %v; want -12.5, nil", got, err)
	}
}

func TestParseLoudnessRejectsUnavailableOrMalformedResponses(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"playabilityStatus":{"status":"UNPLAYABLE"}}`),
		json.RawMessage(`{"playerConfig":{"audioConfig":{}}}`),
		json.RawMessage(`{"streamingData":{"formats":[{"audioTrack":{"loudnessDb":9}}]}}`),
		json.RawMessage(`not json`),
	} {
		if got, err := parseLoudness(raw); err == nil || got != (Loudness{}) {
			t.Errorf("parseLoudness(%s) = %#v, %v; want empty and an error", raw, got, err)
		}
	}
}

func TestParseAudioStreamPicksTheBestDirectAudio(t *testing.T) {
	raw := json.RawMessage(`{"playabilityStatus":{"status":"OK"},"streamingData":{"adaptiveFormats":[
		{"mimeType":"video/mp4; codecs=\"avc1\"","bitrate":900000,"url":"https://v/video","approxDurationMs":"1000"},
		{"mimeType":"audio/mp4; codecs=\"mp4a.40.2\"","bitrate":130000,"url":"https://v/140","approxDurationMs":"231746"},
		{"mimeType":"audio/webm; codecs=\"opus\"","bitrate":200000,"signatureCipher":"s=x","approxDurationMs":"231761"},
		{"mimeType":"audio/webm; codecs=\"opus\"","bitrate":149000,"url":"https://v/251-drc","approxDurationMs":"231761","isDrc":true},
		{"mimeType":"audio/webm; codecs=\"opus\"","bitrate":160000,"url":"https://v/251-dub","approxDurationMs":"231761","audioTrack":{"audioIsDefault":false}},
		{"mimeType":"audio/webm; codecs=\"opus\"","bitrate":148000,"url":"https://v/251","approxDurationMs":"231761","audioTrack":{"audioIsDefault":true}},
		{"mimeType":"audio/webm; codecs=\"opus\"","bitrate":58000,"url":"https://v/250","approxDurationMs":"231761"}]}}`)
	url, length, err := parseAudioStream(raw)
	if err != nil || url != "https://v/251" || length != 231761*time.Millisecond {
		t.Fatalf("parseAudioStream() = %q, %v, %v; want the original-language 148 kb/s opus without DRC, 3m51.761s, nil", url, length, err)
	}
}

func TestParseAudioStreamRejectsUnplayableOrCipheredResponses(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in to confirm you're not a bot"}}`),
		json.RawMessage(`{"playabilityStatus":{"status":"OK"},"streamingData":{"adaptiveFormats":[{"mimeType":"audio/webm","bitrate":1,"signatureCipher":"s=x"}]}}`),
		json.RawMessage(`not json`),
	} {
		if url, _, err := parseAudioStream(raw); err == nil || url != "" {
			t.Errorf("parseAudioStream(%s) = %q, %v; want no URL and an error", raw, url, err)
		}
	}
}

// The player answers a request without a visitor with a bot check, so a
// client that has not browsed yet asks for a visitor first and sends it. The
// URL is only handed on once the media server serves it.
func TestAudioStreamAsksAsAVisitorOfTheVisionOSClient(t *testing.T) {
	var media string
	var refuse atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/youtubei/v1/visitor_id":
			_, _ = w.Write([]byte(`{"responseContext":{"visitorData":"V1"}}`))
		case "/youtubei/v1/player":
			var asked struct {
				Context struct {
					Client struct{ ClientName, VisitorData string }
				}
				VideoID string
			}
			_ = json.NewDecoder(r.Body).Decode(&asked)
			if asked.Context.Client.ClientName != "VISIONOS" || asked.Context.Client.VisitorData != "V1" ||
				r.Header.Get("X-Goog-Visitor-Id") != "V1" || asked.VideoID != "abc" {
				_, _ = w.Write([]byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"playabilityStatus":{"status":"OK"},"streamingData":{"adaptiveFormats":[
				{"mimeType":"audio/webm; codecs=\"opus\"","bitrate":148000,"url":%q,"approxDurationMs":"2000"}]}}`, media)
		case "/media":
			if r.Header.Get("Range") != "bytes=0-0" || refuse.Load() {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusPartialContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	media = server.URL + "/media"
	client := NewClient(Options{BaseURL: server.URL})
	url, length, err := client.AudioStream(context.Background(), "abc")
	if err != nil || url != media || length != 2*time.Second {
		t.Fatalf("AudioStream() = %q, %v, %v; want %s, 2s, nil", url, length, err, media)
	}
	// A URL the media server refuses is no answer: the caller falls back.
	refuse.Store(true)
	if url, _, err := client.AudioStream(context.Background(), "abc"); err == nil {
		t.Fatalf("AudioStream() = %q, nil with the media server refusing; want an error", url)
	}
}

func TestVideoPlaybackUsesWebIdentityAndKeepsMusicUnchanged(t *testing.T) {
	for _, localBase := range []bool{false, true} {
		t.Run(fmt.Sprintf("localBase=%t", localBase), func(t *testing.T) {
			auth, err := NewCookieAuth("SAPISID=test", CookieOptions{AccountIndex: 2, OnBehalfOfUser: "channel"})
			if err != nil {
				t.Fatal(err)
			}
			var requests []*http.Request
			var payloads []map[string]any
			var payloadMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					payloadMu.Lock()
					defer payloadMu.Unlock()
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					payloads = append(payloads, payload)
					_, _ = fmt.Fprintf(w, `{"playbackTracking":{
						"videostatsPlaybackUrl":{"baseUrl":"https://s.youtube.com/start?docid=%s&ei=event&cpn=old"},
						"videostatsWatchtimeUrl":{"baseUrl":"https://s.youtube.com/watchtime?docid=%s&ei=event&cpn=old"}
					}}`, payload["videoId"], payload["videoId"])
				}
			}))
			t.Cleanup(server.Close)
			local, _ := url.Parse(server.URL)
			// Inspect real production URLs/headers, but send every byte only to
			// the local server. No browser cookies or remote account are used.
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, r.Clone(r.Context()))
				forward := r.Clone(r.Context())
				forward.URL.Scheme, forward.URL.Host = local.Scheme, local.Host
				return server.Client().Transport.RoundTrip(forward)
			})
			options := Options{CookieAuth: auth, HTTPClient: &http.Client{Transport: transport}, WebClientVersion: "web-test"}
			webOrigin := "https://www.youtube.com"
			musicOrigin := defaultAPIURL
			if localBase {
				options.BaseURL, options.AllowInsecureCookieAuth = server.URL, true
				webOrigin, musicOrigin = server.URL, server.URL
			}
			client := NewClient(options)
			session, err := client.BeginVideoPlayback(context.Background(), "video", 1250*time.Millisecond, time.Minute)
			if err != nil || session == nil {
				t.Fatalf("BeginVideoPlayback = %v, %v", session, err)
			}
			if err := session.ReportWatchtime(context.Background(), 1250*time.Millisecond, 3*time.Second, time.Minute, "playing", false); err != nil {
				t.Fatal(err)
			}
			// After seeking, only report the new interval, not the skipped gap.
			if err := session.ReportWatchtime(context.Background(), 30*time.Second, 32*time.Second, time.Minute, "paused", true); err != nil {
				t.Fatal(err)
			}
			if err := client.ReportPlayback(context.Background(), "song"); err != nil {
				t.Fatal(err)
			}
			episode, err := client.BeginEpisodePlayback(context.Background(), "episode", 0, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := episode.ReportWatchtime(context.Background(), 0, time.Minute, time.Hour, "playing", false); err != nil {
				t.Fatal(err)
			}
			payloadMu.Lock()
			defer payloadMu.Unlock()
			if len(requests) != 9 || len(payloads) != 3 {
				t.Fatalf("requests=%d payloads=%d", len(requests), len(payloads))
			}
			for i, payload := range payloads {
				identity := "WEB_REMIX"
				if i == 0 {
					identity = "WEB"
				} else if payload["isAudioOnly"] != true {
					t.Errorf("music request lost audio-only: %v", payload)
				}
				ctx := payload["context"].(map[string]any)
				if ctx["client"].(map[string]any)["clientName"] != identity || ctx["user"].(map[string]any)["onBehalfOfUser"] != "channel" {
					t.Errorf("player identity = %v", payload)
				}
			}
			if payloads[0]["isAudioOnly"] != nil || requests[0].Header.Get("X-YouTube-Client-Name") != "1" || requests[0].Header.Get("X-YouTube-Client-Version") != "web-test" {
				t.Errorf("WEB request = %v, %v", payloads[0], requests[0].Header)
			}
			if requests[0].URL.String() != webOrigin+"/youtubei/v1/player" || requests[4].URL.String() != musicOrigin+"/youtubei/v1/player" {
				t.Errorf("player hosts: %s, %s", requests[0].URL, requests[4].URL)
			}
			cpn := requests[1].URL.Query().Get("cpn")
			if len(cpn) != 16 {
				t.Fatalf("nonce = %q", cpn)
			}
			for _, request := range requests {
				if request.Header.Get("Cookie") != "SAPISID=test" || request.Header.Get("X-Goog-Authuser") != "2" || request.Header.Get("X-Goog-PageId") != "channel" {
					t.Errorf("missing auth: %v", request.Header)
				}
			}
			for _, request := range requests[:4] {
				if request.Header.Get("Origin") != webOrigin || request.Header.Get("Referer") != webOrigin+"/" {
					t.Errorf("video origin/referer = %v", request.Header)
				}
				stamp := strings.Split(strings.TrimPrefix(request.Header.Get("Authorization"), "SAPISIDHASH "), "_")[0]
				unix, err := strconv.ParseInt(stamp, 10, 64)
				if err != nil || request.Header.Get("Authorization") != auth.authorization(time.Unix(unix, 0), webOrigin) {
					t.Errorf("authorization has wrong origin: %v", request.Header)
				}
			}
			for _, request := range requests[1:4] {
				q := request.URL.Query()
				if q.Get("c") != "WEB" || q.Get("cpn") != cpn || q.Get("docid") != "video" || q.Get("ei") != "event" || q.Get("len") != "60.000" {
					t.Errorf("video tracking = %v", q)
				}
			}
			if q := requests[1].URL.Query(); q.Get("cmt") != "1.250" || q.Get("rt") != "0" || q.Get("state") != "playing" {
				t.Errorf("start = %v", q)
			}
			for i, want := range []struct{ from, to, state, final string }{{"1.250", "3.000", "playing", ""}, {"30.000", "32.000", "paused", "1"}} {
				q := requests[i+2].URL.Query()
				if q.Get("st") != want.from || q.Get("et") != want.to || q.Get("cmt") != want.to || q.Get("state") != want.state || q.Get("final") != want.final {
					t.Errorf("interval = %v", q)
				}
			}
			for _, index := range []int{5, 7, 8} {
				if requests[index].URL.Query().Get("c") != "WEB_REMIX" || requests[index].Header.Get("Referer") != "https://music.youtube.com/" {
					t.Errorf("music tracking changed: %s, %v", requests[index].URL, requests[index].Header)
				}
			}
		})
	}
}

func TestVideoProgressReadsOnlyTheWebPlayerStartingPosition(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       time.Duration
		wantError  bool
	}{
		{"number", `{"playerConfig":{"playbackStartConfig":{"startSeconds":123.25}}}`, 200, 123250 * time.Millisecond, false},
		{"string", `{"playerConfig":{"playbackStartConfig":{"startSeconds":"42.5"}}}`, 200, 42500 * time.Millisecond, false},
		{"missing", `{}`, 200, 0, false},
		{"missingSeconds", `{"playerConfig":{"playbackStartConfig":{}}}`, 200, 0, false},
		{"unrelated", `{"startSeconds":99,"videoDetails":{"lengthSeconds":"900"}}`, 200, 0, false},
		{"zero", `{"playerConfig":{"playbackStartConfig":{"startSeconds":0}}}`, 200, 0, false},
		{"negative", `{"playerConfig":{"playbackStartConfig":{"startSeconds":-1}}}`, 200, 0, true},
		{"overflow", `{"playerConfig":{"playbackStartConfig":{"startSeconds":1e30}}}`, 200, 0, true},
		{"invalidSeconds", `{"playerConfig":{"playbackStartConfig":{"startSeconds":"bad"}}}`, 200, 0, true},
		{"invalidJSON", `{`, 200, 0, true},
		{"http", `{}`, 403, 0, true},
		{"unplayable", `{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in"}}`, 200, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, err := NewCookieAuth("SAPISID=test", CookieOptions{})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					VideoID string
					Context struct{ Client struct{ ClientName string } }
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/youtubei/v1/player" || payload.VideoID != "video" || payload.Context.Client.ClientName != "WEB" || r.Header.Get("Cookie") != "SAPISID=test" || r.Header.Get("Authorization") == "" {
					t.Errorf("request = %s, %v, %+v", r.URL, r.Header, payload)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			client := NewClient(Options{BaseURL: server.URL, CookieAuth: auth, AllowInsecureCookieAuth: true})
			got, err := client.VideoProgress(context.Background(), "video")
			if got != tc.want || (err != nil) != tc.wantError {
				t.Fatalf("VideoProgress = %v, %v; want %v, error=%t", got, err, tc.want, tc.wantError)
			}
		})
	}
}

func TestSignedOutVideoPlaybackAndProgressDoNotRequest(t *testing.T) {
	client := NewClient(Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("signed-out request")
		return nil, nil
	})}})
	if session, err := client.BeginVideoPlayback(context.Background(), "video", 0, time.Minute); session != nil || err != nil {
		t.Fatalf("signed-out playback = %v, %v", session, err)
	}
	if position, err := client.VideoProgress(context.Background(), "video"); position != 0 || err != nil {
		t.Fatalf("signed-out progress = %v, %v", position, err)
	}
}

func TestVideoPlaybackReturnsPlayerAndTrackingErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, failedPath string
	}{
		{"playerHTTP", `{}`, "/youtubei/v1/player"},
		{"malformedPlayer", `{`, ""},
		{"missingPlayback", `{}`, ""},
		{"missingWatchtime", `{"playbackTracking":{"videostatsPlaybackUrl":{"baseUrl":"https://s.youtube.com/start"}}}`, ""},
		{"startHTTP", "tracking", "/start"},
		{"watchtimeHTTP", "tracking", "/watchtime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, err := NewCookieAuth("SAPISID=test", CookieOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var endpoint string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.failedPath {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if r.URL.Path == "/youtubei/v1/player" {
					if tc.body == "tracking" {
						_, _ = fmt.Fprintf(w, `{"playbackTracking":{
							"videostatsPlaybackUrl":{"baseUrl":%q},
							"videostatsWatchtimeUrl":{"baseUrl":%q}}}`, endpoint+"/start", endpoint+"/watchtime")
					} else {
						_, _ = w.Write([]byte(tc.body))
					}
				}
			}))
			t.Cleanup(server.Close)
			endpoint = server.URL
			client := NewClient(Options{BaseURL: endpoint, CookieAuth: auth, AllowInsecureCookieAuth: true})
			session, err := client.BeginVideoPlayback(context.Background(), "video", 0, time.Minute)
			if tc.failedPath == "/watchtime" {
				if err != nil || session == nil {
					t.Fatalf("BeginVideoPlayback = %v, %v", session, err)
				}
				err = session.ReportWatchtime(context.Background(), 0, time.Second, time.Minute, "paused", true)
			}
			if err == nil {
				t.Fatal("expected player/tracking error")
			}
		})
	}
}

func TestVideoMethodsRejectInvalidIDsAndPropagateCancellation(t *testing.T) {
	auth, err := NewCookieAuth("SAPISID=test", CookieOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	client := NewClient(Options{BaseURL: server.URL, CookieAuth: auth, AllowInsecureCookieAuth: true})
	if session, err := client.BeginVideoPlayback(context.Background(), " ", 0, 0); session != nil || err != nil {
		t.Fatalf("empty video tracking = %v, %v", session, err)
	}
	if _, err := client.VideoProgress(context.Background(), " "); err == nil {
		t.Fatal("empty video progress must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.BeginVideoPlayback(ctx, "video", 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled playback = %v", err)
	}
	if _, err := client.VideoProgress(ctx, "video"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled progress = %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("invalid/canceled requests = %d", got)
	}
}
