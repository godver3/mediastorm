package parsett

import (
	ptt "github.com/itsrenoria/ptt-go"
)

// ParsedTitle represents parsed metadata from a media/torrent title
type ParsedTitle struct {
	Title      string   `json:"title"`
	Year       int      `json:"year,omitempty"`
	IMDBID     string   `json:"imdbId,omitempty"`
	TMDBID     int64    `json:"tmdbId,omitempty"`
	TVDBID     int64    `json:"tvdbId,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
	Quality    string   `json:"quality,omitempty"`
	Codec      string   `json:"codec,omitempty"`
	Audio      []string `json:"audio,omitempty"`
	Channels   []string `json:"channels,omitempty"`
	Group      string   `json:"group,omitempty"`
	Container  string   `json:"container,omitempty"`
	Episodes   []int    `json:"episodes,omitempty"`
	Seasons    []int    `json:"seasons,omitempty"`
	Languages  []string `json:"languages,omitempty"`
	Extended   bool     `json:"extended,omitempty"`
	Hardcoded  bool     `json:"hardcoded,omitempty"`
	Proper     bool     `json:"proper,omitempty"`
	Repack     bool     `json:"repack,omitempty"`
	Complete   bool     `json:"complete,omitempty"`
	Volumes    []int    `json:"volumes,omitempty"`
	Site       string   `json:"site,omitempty"`
	BitDepth   string   `json:"bit_depth,omitempty"`
	HDR        []string `json:"hdr,omitempty"`
}

// fromTorrentInfo converts ptt-go's TorrentInfo to our ParsedTitle
func fromTorrentInfo(info *ptt.TorrentInfo) *ParsedTitle {
	if info == nil {
		return nil
	}
	return &ParsedTitle{
		Title:      info.Title,
		Year:       info.Year,
		Resolution: info.Resolution,
		Quality:    info.Quality,
		Codec:      info.Codec,
		Audio:      info.Audio,
		Channels:   info.Channels,
		Group:      info.Group,
		Container:  info.Container,
		Episodes:   info.Episodes,
		Seasons:    info.Seasons,
		Languages:  info.Languages,
		Hardcoded:  info.Hardcoded,
		Proper:     info.Proper,
		Repack:     info.Repack,
		Complete:   info.Complete,
		Volumes:    info.Volumes,
		Site:       info.Site,
		BitDepth:   info.BitDepth,
		HDR:        info.HDR,
	}
}

// ParseTitle parses a single media title using ptt-go (native Go, no subprocess)
func ParseTitle(title string) (*ParsedTitle, error) {
	return fromTorrentInfo(ptt.Parse(title)), nil
}

// ParseTitleBatch parses multiple titles and returns a map of title -> parsed result
func ParseTitleBatch(titles []string) (map[string]*ParsedTitle, error) {
	resultMap := make(map[string]*ParsedTitle, len(titles))
	for _, title := range titles {
		resultMap[title] = fromTorrentInfo(ptt.Parse(title))
	}
	return resultMap, nil
}
