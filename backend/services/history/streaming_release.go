package history

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"novastream/models"
)

// TVDB's airsTime describes a show's linear broadcast. Some shows also (or only)
// release on a streaming service, earlier: Airport Brazil airs at 21:15 on
// Discovery Brasil but is on discovery+ that morning. TVmaze files such shows under
// their web channel with no schedule time. For those, an episode counts as aired
// from the start of its air date in the show's timezone rather than at the
// broadcast time, so it leaves My Upcoming for Continue Watching on release day.
func (s *Service) applyStreamingRelease(ctx context.Context, title models.Title, ids map[string]string, ep *models.EpisodeReference) {
	if ep.AirDateTimeUTC == "" || title.AirsTimezone == "" {
		return
	}
	loc, err := time.LoadLocation(title.AirsTimezone)
	if err != nil {
		return
	}
	day, err := time.ParseInLocation("2006-01-02", ep.AirDate, loc)
	if err != nil || day.Before(time.Now().Add(-48*time.Hour)) || day.After(time.Now().Add(7*24*time.Hour)) {
		return
	}
	scheduled, err := time.Parse(time.RFC3339, ep.AirDateTimeUTC)
	if err != nil || !day.Before(scheduled) {
		return
	}
	imdb := continueWatchingIMDB(title, ids)
	if imdb == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if s.tvmazeClient().streamingWithoutTime(ctx, imdb) {
		ep.AirDateTimeUTC = day.UTC().Format(time.RFC3339)
	}
}

// streamingWithoutTime reports whether TVmaze lists the show on a web channel
// only, with no schedule time. The lookup shares the airtime client's cache.
func (c *tvmazeAirtimeClient) streamingWithoutTime(ctx context.Context, imdb string) bool {
	body := c.get(ctx, "/lookup/shows?imdb="+url.QueryEscape(imdb), 30*24*time.Hour)
	var show struct {
		Network    *struct{} `json:"network"`
		WebChannel *struct{} `json:"webChannel"`
		Schedule   struct {
			Time string `json:"time"`
		} `json:"schedule"`
	}
	if json.Unmarshal(body, &show) != nil {
		return false
	}
	return show.Network == nil && show.WebChannel != nil && strings.TrimSpace(show.Schedule.Time) == ""
}
