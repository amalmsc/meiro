package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// VideoItem is a regular YouTube video, independent of the music catalogue.
// Duration and Views retain YouTube's localized display text; live videos may
// have no duration. Missing metadata is left empty.
type VideoItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Channel   string `json:"channel"`
	Thumbnail string `json:"thumbnail"`
	Duration  string `json:"duration"`
	Views     string `json:"views"`
}

// VideoResult is one page of regular YouTube videos. Its opaque continuation
// must be passed to the matching ContinueVideos or ContinueVideoHome method,
// not the music catalogue's ContinueSearch.
type VideoResult struct {
	Items             []VideoItem `json:"items"`
	ContinuationToken string      `json:"continuationToken,omitempty"`
}

// GetVideoHome fetches the selected account/channel's personalized YouTube
// home feed as WEB, using www.youtube.com (or the client's explicit test host).
// Signed-out clients return an error without making a request.
func (c *Client) GetVideoHome(ctx context.Context) (*VideoResult, error) {
	return c.videoHome(ctx, map[string]any{"browseId": "FEwhat_to_watch"}, false)
}

// ContinueVideoHome fetches the next home page on the same signed-in client.
// The token must come from GetVideoHome or ContinueVideoHome, not video search.
func (c *Client) ContinueVideoHome(ctx context.Context, token string) (*VideoResult, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("youtube: video home continuation token is required")
	}
	return c.videoHome(ctx, map[string]any{"continuation": token}, true)
}

func (c *Client) videoHome(ctx context.Context, payload map[string]any, continuation bool) (*VideoResult, error) {
	if c.cookieAuth == nil {
		return nil, errors.New("youtube: video home requires sign-in")
	}
	raw, err := c.videoPlaybackClient().executeAs(ctx, webClient, false, "browse", payload)
	if err != nil {
		return nil, err
	}
	root := decodeResponse(raw)
	if videoPath(root, "error") != nil {
		return nil, errors.New("youtube: video home returned an API error")
	}
	// An empty grid/page is valid, but an unexpected response (including a
	// sign-in page or an API error served with HTTP 200) isn't an empty feed.
	valid := false
	if continuation {
		for _, key := range []string{"onResponseReceivedCommands", "onResponseReceivedActions", "onResponseReceivedEndpoints"} {
			actions, _ := videoPath(root, key).([]any)
			for _, action := range actions {
				for _, command := range []string{"appendContinuationItemsAction", "reloadContinuationItemsCommand"} {
					container := videoPath(action, command)
					if target := videoString(container, "targetId"); target == "" || videoHomeTarget(target) {
						if _, ok := videoPath(container, "continuationItems").([]any); ok {
							valid = true
						}
					}
				}
			}
		}
	} else {
		tabs, _ := videoPath(root, "contents", "twoColumnBrowseResultsRenderer", "tabs").([]any)
		for _, tab := range tabs {
			if videoPath(tab, "tabRenderer", "selected") != true {
				continue
			}
			if _, ok := videoPath(tab, "tabRenderer", "content", "richGridRenderer", "contents").([]any); ok {
				valid = true
			}
		}
	}
	if !valid {
		return nil, errors.New("youtube: malformed video home response")
	}
	return parseVideoResult(raw), nil
}

func videoHomeTarget(target string) bool {
	return target == "browse-feedFEwhat_to_watch"
}

// SearchVideos searches regular YouTube for videos only. Callers should use a
// dedicated signed-out Client with BaseURL https://www.youtube.com. The client's
// configured host is retained (including local test servers).
func (c *Client) SearchVideos(ctx context.Context, query string) (*VideoResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("youtube: video search query is required")
	}
	// The WEB search filter encodes type=video, rather than a music category.
	raw, err := c.executeAs(ctx, webClient, false, "search", map[string]any{
		"query": query, "params": "EgIQAQ%3D%3D",
	})
	if err != nil {
		return nil, err
	}
	return parseVideoResult(raw), nil
}

// ContinueVideos requests the next page from a VideoResult on the same client,
// keeping the WEB identity and search endpoint that issued the token.
func (c *Client) ContinueVideos(ctx context.Context, token string) (*VideoResult, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("youtube: video search continuation token is required")
	}
	raw, err := c.executeAs(ctx, webClient, false, "search", map[string]any{"continuation": token})
	if err != nil {
		return nil, err
	}
	return parseVideoResult(raw), nil
}

func videoPath(value any, keys ...string) any {
	for _, key := range keys {
		object, ok := value.(*jsonObject)
		if !ok {
			return nil
		}
		value = object.get(key)
	}
	return value
}

func videoString(value any, keys ...string) string {
	text, _ := videoPath(value, keys...).(string)
	return text
}

func parseVideoResult(raw json.RawMessage) *VideoResult {
	root := decodeResponse(raw)
	result := &VideoResult{Items: []VideoItem{}}
	seen := make(map[string]bool)
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				walk(child)
			}
		case *jsonObject:
			for _, member := range node.members {
				var item VideoItem
				switch member.key {
				case "videoRenderer":
					item = parseVideoRenderer(member.value)
				case "lockupViewModel":
					item = parseVideoLockup(member.value)
				case "continuationItemRenderer":
					if result.ContinuationToken == "" {
						result.ContinuationToken = videoString(member.value, "continuationEndpoint", "continuationCommand", "token")
					}
				case "richShelfRenderer":
					if videoPath(member.value, "isShorts") != true {
						walk(videoPath(member.value, "contents"))
						walk(videoPath(member.value, "content"))
					}
				case "itemSectionRenderer", "sectionListRenderer", "richGridRenderer", "richItemRenderer", "shelfRenderer":
					walk(videoPath(member.value, "contents"))
					walk(videoPath(member.value, "content"))
				case "verticalListRenderer", "horizontalListRenderer", "expandedShelfContentsRenderer":
					walk(videoPath(member.value, "items"))
				}
				// Deliberately do not descend into arbitrary renderers: ads,
				// playlists, channels, menus and filter chips aren't results.
				if item.ID != "" && !seen[item.ID] {
					seen[item.ID] = true
					result.Items = append(result.Items, item)
				}
			}
		}
	}
	walk(videoPath(root, "contents", "twoColumnSearchResultsRenderer", "primaryContents"))
	tabs, _ := videoPath(root, "contents", "twoColumnBrowseResultsRenderer", "tabs").([]any)
	for _, tab := range tabs {
		if videoPath(tab, "tabRenderer", "selected") == true {
			walk(videoPath(tab, "tabRenderer", "content", "richGridRenderer", "contents"))
		}
	}
	for _, key := range []string{"onResponseReceivedCommands", "onResponseReceivedActions", "onResponseReceivedEndpoints"} {
		actions, _ := videoPath(root, key).([]any)
		for _, action := range actions {
			for _, command := range []string{"appendContinuationItemsAction", "reloadContinuationItemsCommand"} {
				container := videoPath(action, command)
				target := videoString(container, "targetId")
				if target == "" || target == "search-feed" || videoHomeTarget(target) {
					walk(videoPath(container, "continuationItems"))
				}
			}
		}
	}
	return result
}

func parseVideoRenderer(renderer any) VideoItem {
	if videoPath(renderer, "navigationEndpoint", "reelWatchEndpoint") != nil ||
		videoString(renderer, "navigationEndpoint", "commandMetadata", "webCommandMetadata", "webPageType") == "WEB_PAGE_TYPE_SHORTS" {
		return VideoItem{}
	}
	item := VideoItem{
		ID: videoString(renderer, "videoId"), Title: rendererText(videoPath(renderer, "title")),
		Duration: rendererText(videoPath(renderer, "lengthText")), Views: rendererText(videoPath(renderer, "viewCountText")),
		Thumbnail: videoThumbnail(videoPath(renderer, "thumbnail", "thumbnails")),
	}
	for _, key := range []string{"ownerText", "longBylineText", "shortBylineText"} {
		if item.Channel = rendererText(videoPath(renderer, key)); item.Channel != "" {
			break
		}
	}
	if item.Views == "" {
		item.Views = rendererText(videoPath(renderer, "shortViewCountText"))
	}
	if item.Duration == "" {
		if overlay := findRenderer(videoPath(renderer, "thumbnailOverlays"), "thumbnailOverlayTimeStatusRenderer"); overlay != nil {
			item.Duration = rendererText(overlay.get("text"))
		}
	}
	return item
}

func parseVideoLockup(renderer any) VideoItem {
	if videoString(renderer, "contentType") != "LOCKUP_CONTENT_TYPE_VIDEO" {
		return VideoItem{}
	}
	metadata := videoPath(renderer, "metadata", "lockupMetadataViewModel")
	item := VideoItem{
		ID: videoString(renderer, "contentId"), Title: videoString(metadata, "title", "content"),
		Thumbnail: videoThumbnail(videoPath(renderer, "contentImage", "thumbnailViewModel", "image", "sources")),
	}
	// The first metadata row credits the channel, the second gives views
	// followed by publication time. Do not guess from localized text.
	rows, _ := videoPath(metadata, "metadata", "contentMetadataViewModel", "metadataRows").([]any)
	for index, row := range rows {
		parts, _ := videoPath(row, "metadataParts").([]any)
		if len(parts) == 0 {
			continue
		}
		text := videoString(parts[0], "text", "content")
		switch index {
		case 0:
			item.Channel = text
		case 1:
			item.Views = text
		}
	}
	if badge := findRenderer(videoPath(renderer, "contentImage"), "thumbnailBadgeViewModel"); badge != nil {
		item.Duration = videoString(badge, "text")
	}
	return item
}

// YouTube orders thumbnail sources from small to large.
func videoThumbnail(value any) string {
	sources, _ := value.([]any)
	for index := len(sources) - 1; index >= 0; index-- {
		if url := videoString(sources[index], "url"); url != "" {
			return url
		}
	}
	return ""
}
