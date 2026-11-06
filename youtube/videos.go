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

// VideoResult is one page of regular YouTube search results. Its opaque
// continuation is for ContinueVideos, not the music catalogue's ContinueSearch.
type VideoResult struct {
	Items             []VideoItem `json:"items"`
	ContinuationToken string      `json:"continuationToken,omitempty"`
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
				case "itemSectionRenderer", "sectionListRenderer", "richItemRenderer", "shelfRenderer", "richShelfRenderer":
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
	for _, key := range []string{"onResponseReceivedCommands", "onResponseReceivedActions", "onResponseReceivedEndpoints"} {
		actions, _ := videoPath(root, key).([]any)
		for _, action := range actions {
			for _, command := range []string{"appendContinuationItemsAction", "reloadContinuationItemsCommand"} {
				container := videoPath(action, command)
				target := videoString(container, "targetId")
				if target == "" || target == "search-feed" {
					walk(videoPath(container, "continuationItems"))
				}
			}
		}
	}
	return result
}

func parseVideoRenderer(renderer any) VideoItem {
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
