package betterlyrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Translation is one lyric line in another language, with its romanization
// when the source is not already written in Latin letters.
type Translation struct {
	Text, Romanization string
}

type translateRequest struct {
	Lines   []string `json:"lines"`
	To      string   `json:"to"`
	VideoID string   `json:"videoId,omitempty"`
}

type translatedLine struct {
	Translation    *string `json:"translation"`
	Romanization   *string `json:"romanization"`
	NeedsTranslate bool    `json:"needsTranslation"`
}

type translateResponse struct {
	Lines []translatedLine `json:"lines"`
	Error string           `json:"error"`
}

// Translate asks Unison for an English reading of lines. Blank lines keep
// their place. A line already in English, or one Unison cannot read, comes
// back empty rather than repeating the original.
func (c *Client) Translate(ctx context.Context, videoID string, lines []string) ([]Translation, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(translateRequest{Lines: lines, To: "en", VideoID: videoID})
	if err != nil {
		return nil, err
	}
	base := c.BaseURL
	if base == "" {
		base = "https://unison.boidu.dev"
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/translate", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", userAgent)
	got, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer got.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(got.Body, maxBody))
	if err != nil {
		return nil, err
	}
	var answer translateResponse
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, errors.New("betterlyrics: unreadable translation")
	}
	if got.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("betterlyrics: translation %s", got.Status)
	}
	if len(answer.Lines) != len(lines) {
		return nil, errors.New("betterlyrics: translation has the wrong number of lines")
	}
	out := make([]Translation, len(lines))
	for i, line := range answer.Lines {
		if line.NeedsTranslate && line.Translation != nil {
			out[i].Text = strings.TrimSpace(*line.Translation)
		}
		if line.Romanization != nil {
			out[i].Romanization = strings.TrimSpace(*line.Romanization)
		}
	}
	return out, nil
}
