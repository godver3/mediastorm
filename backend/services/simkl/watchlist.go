package simkl

// PlanToWatchStatus is Simkl's watchlist ("Plan to Watch") status bucket.
const PlanToWatchStatus = "plantowatch"

// WatchlistEntry identifies one movie or show in a Simkl watchlist write.
// Simkl resolves whichever IDs it recognises, so send every ID available.
// Leave Title empty for exports: with a title Simkl fuzzy-matches unknown IDs.
type WatchlistEntry struct {
	To    string `json:"to,omitempty"`
	Title string `json:"title,omitempty"`
	Year  int    `json:"year,omitempty"`
	IDs   IDs    `json:"ids"`
}

type watchlistRequest struct {
	Movies []WatchlistEntry `json:"movies,omitempty"`
	Shows  []WatchlistEntry `json:"shows,omitempty"`
}

// WatchlistWriteResponse is the add-to-list result envelope. Simkl answers 201
// even when some items could not be matched; those land in NotFound.
type WatchlistWriteResponse struct {
	NotFound struct {
		Movies []WatchlistEntry `json:"movies"`
		Shows  []WatchlistEntry `json:"shows"`
	} `json:"not_found"`
}

// NotFoundCount returns how many requested items Simkl could not match.
func (r *WatchlistWriteResponse) NotFoundCount() int {
	if r == nil {
		return 0
	}
	return len(r.NotFound.Movies) + len(r.NotFound.Shows)
}

// AddToPlanToWatch moves items into the user's Plan to Watch list via
// POST /sync/add-to-list. Anime are accepted in the shows array.
func (c *Client) AddToPlanToWatch(clientID, accessToken string, movies, shows []WatchlistEntry) (*WatchlistWriteResponse, error) {
	req := watchlistRequest{Movies: withListStatus(movies, PlanToWatchStatus), Shows: withListStatus(shows, PlanToWatchStatus)}
	if len(req.Movies) == 0 && len(req.Shows) == 0 {
		return &WatchlistWriteResponse{}, nil
	}
	var out WatchlistWriteResponse
	if _, err := c.post(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/add-to-list", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveFromWatchlist un-tracks items via POST /sync/history/remove, Simkl's
// documented way to take an item off its lists. Only call it for items that
// sit in Plan to Watch: on a watched item it would also erase its history.
func (c *Client) RemoveFromWatchlist(clientID, accessToken string, movies, shows []WatchlistEntry) error {
	req := watchlistRequest{Movies: withListStatus(movies, ""), Shows: withListStatus(shows, "")}
	if len(req.Movies) == 0 && len(req.Shows) == 0 {
		return nil
	}
	_, err := c.post(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/history/remove", req, nil)
	return err
}

func withListStatus(entries []WatchlistEntry, status string) []WatchlistEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]WatchlistEntry, len(entries))
	for i, entry := range entries {
		entry.To = status
		out[i] = entry
	}
	return out
}
