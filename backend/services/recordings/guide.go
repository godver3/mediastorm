package recordings

import (
	"net/url"
	"slices"
	"strings"
	"time"

	"novastream/models"
)

// guideMatchWindow is how far a guide programme's start may be from a
// recording's start (clients round, guides shift by a minute).
const guideMatchWindow = 2 * time.Minute

// guideFromProgram keeps what the guide says about a programme; nil when it
// says nothing beyond title and description.
func guideFromProgram(p models.EPGProgram) *models.RecordingGuide {
	guide := models.RecordingGuide{
		Image:        guideImageURL(p.Icon),
		EpisodeTitle: strings.TrimSpace(p.SubTitle),
		Episode:      strings.TrimSpace(p.Episode),
		Year:         p.Year,
		Categories:   slices.Clone(p.Categories),
		Rating:       strings.TrimSpace(p.Rating),
	}
	if guide.Image == "" && guide.EpisodeTitle == "" && guide.Episode == "" && guide.Year == 0 && len(guide.Categories) == 0 && guide.Rating == "" {
		return nil
	}
	return &guide
}

// guideFor finds the guide programme a recording was scheduled from, on its
// channel at its start (the guide's time correction for the profile
// applied), preferring one with the same title.
func (s *Service) guideFor(profileID, tvgID string, start time.Time, title string) *models.RecordingGuide {
	tvgID = strings.TrimSpace(tvgID)
	if s.epg == nil || tvgID == "" {
		return nil
	}
	offset := time.Duration(0)
	if s.epgoffset != nil {
		offset = s.epgoffset(profileID)
	}
	at := start.Add(-offset)
	var best *models.EPGProgram
	for _, programs := range s.epg.GetScheduleMultiple([]string{tvgID}, at.Add(-guideMatchWindow), at.Add(guideMatchWindow)) {
		for i := range programs {
			p := &programs[i]
			d := p.Start.Sub(at)
			if d < -guideMatchWindow || d > guideMatchWindow {
				continue
			}
			if best == nil || (strings.EqualFold(p.Title, title) && !strings.EqualFold(best.Title, title)) {
				best = p
			}
		}
	}
	if best == nil {
		return nil
	}
	return guideFromProgram(*best)
}

// guideImageURL keeps a guide image URL only when it is an absolute
// http(s) URL; anything else is dropped.
func guideImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 2048 {
		return ""
	}
	return raw
}
