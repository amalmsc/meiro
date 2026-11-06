package youtube

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Loudness is the per-track loudness metadata returned by YouTube's web player.
// LoudnessDB is measured in decibels relative to the reference level.
type Loudness struct {
	LoudnessDB float64
}

func parseLoudness(raw json.RawMessage) (Loudness, error) {
	var response struct {
		PlayabilityStatus struct {
			Status string `json:"status"`
		} `json:"playabilityStatus"`
		PlayerConfig struct {
			AudioConfig struct {
				PerceptualLoudnessDb float64 `json:"perceptualLoudnessDb"`
				LoudnessDb           float64 `json:"loudnessDb"`
			} `json:"audioConfig"`
		} `json:"playerConfig"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return Loudness{}, err
	}
	if response.PlayabilityStatus.Status == "UNPLAYABLE" {
		return Loudness{}, errors.New("youtube: player response is unplayable")
	}
	audio := response.PlayerConfig.AudioConfig
	if audio.LoudnessDb != 0 {
		return Loudness{LoudnessDB: audio.LoudnessDb}, nil
	}
	if audio.PerceptualLoudnessDb != 0 {
		return Loudness{LoudnessDB: audio.PerceptualLoudnessDb}, nil
	}
	return Loudness{}, errors.New("youtube: player response has no loudness metadata")
}

// Loudness asks the web player for the loudness metadata of videoID. YouTube
// only returns this metadata for sessions it accepts, so an unavailable value
// is reported as an error and can be retried later.
func (c *Client) Loudness(ctx context.Context, videoID string) (Loudness, error) {
	if c.cookieAuth == nil {
		return Loudness{}, errors.New("youtube: loudness requires a signed-in session")
	}
	if strings.TrimSpace(videoID) == "" {
		return Loudness{}, errors.New("youtube: loudness requires a video ID")
	}
	raw, err := c.executeAs(ctx, webClient, false, "player", map[string]any{
		"videoId":         videoID,
		"playbackContext": map[string]any{"contentPlaybackContext": map[string]any{}},
	})
	if err != nil {
		return Loudness{}, err
	}
	return parseLoudness(raw)
}

// AudioStream returns a direct URL of the best audio of videoID and the
// track's length, as yt-dlp resolves it with its visionOS client, without
// running yt-dlp. It asks anonymously, as a visitor: a client with a
// signed-in session is refused.
func (c *Client) AudioStream(ctx context.Context, videoID string) (string, time.Duration, error) {
	if c.cookieAuth != nil {
		return "", 0, errors.New("youtube: audio streams are resolved anonymously")
	}
	if strings.TrimSpace(videoID) == "" {
		return "", 0, errors.New("youtube: audio stream requires a video ID")
	}
	// The player answers a request without a visitor with a bot check.
	if c.currentVisitorData() == "" {
		if _, err := c.executeAs(ctx, musicClient, false, "visitor_id", map[string]any{}); err != nil {
			return "", 0, err
		}
	}
	raw, err := c.executeAs(ctx, visionOSClient, false, "player", map[string]any{
		"videoId": videoID, "contentCheckOk": true, "racyCheckOk": true,
	})
	if err != nil {
		return "", 0, err
	}
	stream, length, err := parseAudioStream(raw)
	if err != nil {
		return "", 0, err
	}
	// The media server now and then refuses a URL the player just gave out.
	// Asking for its first byte finds that out here, where the caller can
	// still resolve it another way, rather than in the player.
	if err := c.probe(ctx, stream); err != nil {
		return "", 0, err
	}
	return stream, length, nil
}

// probe asks for the first byte of a media URL, as the player asks for it.
func (c *Client) probe(ctx context.Context, media string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, media, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", browserUserAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("youtube: probe audio stream: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("youtube: audio stream answers HTTP %d", resp.StatusCode)
	}
	return nil
}

// parseAudioStream picks the audio-only format of the highest bitrate that
// has a direct URL, as yt-dlp's bestaudio does: of the track's own language,
// and with its dynamic range left as it was mixed rather than compressed
// (DRC), which YouTube lists beside it at a slightly higher bitrate.
func parseAudioStream(raw json.RawMessage) (string, time.Duration, error) {
	var response struct {
		PlayabilityStatus struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"playabilityStatus"`
		StreamingData struct {
			AdaptiveFormats []struct {
				MimeType         string `json:"mimeType"`
				Bitrate          int    `json:"bitrate"`
				URL              string `json:"url"`
				ApproxDurationMs string `json:"approxDurationMs"`
				IsDrc            bool   `json:"isDrc"`
				AudioTrack       *struct {
					AudioIsDefault bool `json:"audioIsDefault"`
				} `json:"audioTrack"`
			} `json:"adaptiveFormats"`
		} `json:"streamingData"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", 0, err
	}
	if status := response.PlayabilityStatus; status.Status != "OK" {
		return "", 0, fmt.Errorf("youtube: player says %s: %s", status.Status, status.Reason)
	}
	best, bitrate, length := "", -1, time.Duration(0)
	for _, format := range response.StreamingData.AdaptiveFormats {
		if !strings.HasPrefix(format.MimeType, "audio/") || format.URL == "" || format.Bitrate <= bitrate ||
			format.IsDrc || format.AudioTrack != nil && !format.AudioTrack.AudioIsDefault {
			continue
		}
		best, bitrate = format.URL, format.Bitrate
		ms, _ := strconv.ParseInt(format.ApproxDurationMs, 10, 64)
		length = time.Duration(ms) * time.Millisecond
	}
	if best == "" {
		return "", 0, errors.New("youtube: player lists no audio with a direct URL")
	}
	return best, length, nil
}

// ReportPlayback tells YouTube Music that the account started playing a track,
// as its own player does. That is what puts the track in the listening
// history, and the history is what the recommendations are made from; Meiro
// plays through yt-dlp, so nothing else reports it. It needs a signed-in
// session, and does nothing without one.
func (c *Client) ReportPlayback(ctx context.Context, videoID string) error {
	_, err := c.reportPlayback(ctx, videoID, 0, 0, false, musicClient)
	return err
}

// PlaybackSession is YouTube's tracking session for one authenticated video
// or podcast episode. Its nonce and watch-time URL must stay together: YouTube
// drops progress pings that do not belong to the playback ping that started it.
type PlaybackSession struct {
	client       *Client
	watchtimeURL *url.URL
	cpn          string
	startedAt    time.Time
	identity     innerTubeClient
}

// BeginEpisodePlayback starts an authenticated tracking session for an
// episode at position. ReportWatchtime sends intervals through this same
// session so YouTube can update the account's episode progress.
func (c *Client) BeginEpisodePlayback(ctx context.Context, videoID string, position, duration time.Duration) (*PlaybackSession, error) {
	return c.reportPlayback(ctx, videoID, position, duration, true, musicClient)
}

// BeginVideoPlayback starts authenticated WEB tracking for a regular YouTube
// video. Signed-out clients and empty video IDs return a nil session without
// making requests, just like BeginEpisodePlayback.
func (c *Client) BeginVideoPlayback(ctx context.Context, videoID string, position, duration time.Duration) (*PlaybackSession, error) {
	return c.videoPlaybackClient().reportPlayback(ctx, videoID, position, duration, true, webClient)
}

// videoPlaybackClient keeps the Music client unchanged, and retains explicit
// local endpoints for tests. Do not copy Client: it contains a visitor mutex.
func (c *Client) videoPlaybackClient() *Client {
	base := c.baseURL
	if base == defaultAPIURL {
		base = "https://www.youtube.com"
	}
	return NewClient(Options{
		HTTPClient: c.httpClient, BaseURL: base, APIKey: c.apiKey,
		ClientVersion: c.clientVersion, WebClientVersion: c.webClientVersion,
		VisitorData: c.currentVisitorData(), Language: c.language, Country: c.country,
		CookieAuth: c.cookieAuth, AllowInsecureCookieAuth: c.allowInsecureCookieAuth,
	})
}

// VideoProgress returns the signed-in WEB player's suggested starting position,
// in seconds from the video's start. An absent playbackStartConfig/startSeconds
// means no resume position (zero). Signed-out clients return zero without a
// request; invalid IDs, failed requests and malformed responses return errors.
func (c *Client) VideoProgress(ctx context.Context, videoID string) (time.Duration, error) {
	if c.cookieAuth == nil {
		return 0, nil
	}
	if strings.TrimSpace(videoID) == "" {
		return 0, errors.New("youtube: video progress requires a video ID")
	}
	raw, err := c.videoPlaybackClient().executeAs(ctx, webClient, false, "player", map[string]any{
		"videoId":         videoID,
		"playbackContext": map[string]any{"contentPlaybackContext": map[string]any{}},
	})
	if err != nil {
		return 0, err
	}
	var response struct {
		PlayabilityStatus struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"playabilityStatus"`
		PlayerConfig struct {
			PlaybackStartConfig struct {
				StartSeconds json.Number `json:"startSeconds"`
			} `json:"playbackStartConfig"`
		} `json:"playerConfig"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return 0, err
	}
	if status := response.PlayabilityStatus; status.Status != "" && status.Status != "OK" {
		return 0, fmt.Errorf("youtube: player says %s: %s", status.Status, status.Reason)
	}
	start := response.PlayerConfig.PlaybackStartConfig.StartSeconds
	if start == "" {
		return 0, nil
	}
	seconds, err := start.Float64()
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return 0, errors.New("youtube: invalid video resume position")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func (c *Client) reportPlayback(ctx context.Context, videoID string, position, duration time.Duration, withWatchtime bool, identity innerTubeClient) (*PlaybackSession, error) {
	if c.cookieAuth == nil || strings.TrimSpace(videoID) == "" {
		return nil, nil
	}
	raw, err := c.executeAs(ctx, identity, identity == musicClient, "player", map[string]any{
		"videoId":         videoID,
		"playbackContext": map[string]any{"contentPlaybackContext": map[string]any{}},
	})
	if err != nil {
		return nil, err
	}
	root := decodeResponse(raw)
	playbackURL, err := trackingURL(root, "videostatsPlaybackUrl")
	if err != nil {
		return nil, err
	}
	var watchtimeURL *url.URL
	if withWatchtime {
		watchtimeURL, err = trackingURL(root, "videostatsWatchtimeUrl")
		if err != nil {
			return nil, err
		}
	}
	cpn := clientPlaybackNonce()
	params := url.Values{
		"ver": {"2"},
		"c":   {defaultMusicContext},
		"cpn": {cpn},
	}
	if identity == webClient {
		params.Set("c", string(webClient))
	}
	if withWatchtime {
		params.Set("state", "playing")
		params.Set("cmt", seconds(position))
		params.Set("len", seconds(duration))
		params.Set("rt", "0")
	}
	if err := c.pingTracking(ctx, playbackURL, params, "playback", identity); err != nil {
		return nil, err
	}
	if !withWatchtime {
		return nil, nil
	}
	return &PlaybackSession{client: c, watchtimeURL: watchtimeURL, cpn: cpn, startedAt: time.Now(), identity: identity}, nil
}

// ReportWatchtime reports the newly played interval. Positions use seconds
// from the video's start; seeking is handled by starting a new interval at
// the position reached, never by reporting the skipped section.
func (s *PlaybackSession) ReportWatchtime(ctx context.Context, from, to, duration time.Duration, state string, final bool) error {
	if s == nil || s.client == nil || s.watchtimeURL == nil {
		return nil
	}
	params := url.Values{
		"ver":   {"2"},
		"c":     {defaultMusicContext},
		"cpn":   {s.cpn},
		"st":    {seconds(from)},
		"et":    {seconds(to)},
		"cmt":   {seconds(to)},
		"len":   {seconds(duration)},
		"state": {state},
		"rt":    {seconds(time.Since(s.startedAt))},
	}
	if final {
		params.Set("final", "1")
	}
	if s.identity == webClient {
		params.Set("c", string(webClient))
	}
	return s.client.pingTracking(ctx, s.watchtimeURL, params, "watchtime", s.identity)
}

func trackingURL(root any, key string) (*url.URL, error) {
	tracking := findRenderer(root, key)
	base, _ := tracking.get("baseUrl").(string)
	if base == "" {
		return nil, errors.New("youtube: player response has no " + key)
	}
	target, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	return target, nil
}

func (c *Client) pingTracking(ctx context.Context, target *url.URL, params url.Values, operation string, identity innerTubeClient) error {
	if !c.allowInsecureCookieAuth && !isYouTubeURL(target) {
		return errors.New("youtube: refusing to send cookie authentication to a non-YouTube or non-HTTPS URL")
	}
	query := target.Query()
	// Keep the session's tracking parameters as YouTube returned them, while
	// replacing the values that identify this client playback.
	for key, values := range params {
		query.Del(key)
		for _, value := range values {
			query.Add(key, value)
		}
	}
	requestURL := *target
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Referer", "https://music.youtube.com/")
	c.setIdentity(req, c.currentVisitorData())
	if identity == webClient {
		base, err := url.Parse(c.baseURL)
		if err != nil {
			return err
		}
		origin := base.Scheme + "://" + base.Host
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
		if c.cookieAuth != nil {
			req.Header.Set("Authorization", c.cookieAuth.authorization(time.Now(), origin))
		}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(operation, resp)
	}
	return nil
}

func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}

// clientPlaybackNonce is the 16 characters the player sends to tell one
// playback from another.
func clientPlaybackNonce() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[b[i]&63]
	}
	return string(b)
}
