package epg

import (
	"runtime/debug"

	"novastream/models"
)

// compactSchedule reduces the in-memory footprint of a guide without changing
// its contents. Large IPTV guides hold hundreds of thousands of programs, and
// both JSON decoding and XMLTV parsing leave every program with its own copy
// of strings that repeat heavily (channel IDs, titles, categories, icons,
// ratings, rerun descriptions) and with slices over-allocated by append
// growth. It is safe to call on a schedule no one else is reading yet.
func compactSchedule(schedule *models.EPGSchedule) {
	if schedule == nil {
		return
	}
	in := newStringInterner()

	for id, ch := range schedule.Channels {
		ch.ID = in.intern(ch.ID)
		ch.Name = in.intern(ch.Name)
		ch.Icon = in.intern(ch.Icon)
		ch.Aliases = in.internSlice(ch.Aliases)
		schedule.Channels[id] = ch
	}

	categorySets := make(map[string][]string)
	for channelID, programs := range schedule.Programs {
		if len(programs) == 0 {
			// Keep the key (it marks a known channel) but drop any backing array.
			schedule.Programs[channelID] = nil
			continue
		}
		key := in.intern(channelID)
		exact := make([]models.EPGProgram, len(programs))
		copy(exact, programs)
		for i := range exact {
			p := &exact[i]
			p.ChannelID = in.intern(p.ChannelID)
			p.Title = in.intern(p.Title)
			p.Description = in.intern(p.Description)
			p.Icon = in.intern(p.Icon)
			p.Episode = in.intern(p.Episode)
			p.SubTitle = in.intern(p.SubTitle)
			p.Rating = in.intern(p.Rating)
			p.Categories = internCategories(in, categorySets, p.Categories)
		}
		schedule.Programs[key] = exact
	}
}

// internCategories shares one backing array between programs with identical
// category lists. The returned slices are capacity-clipped so an append by a
// caller cannot write into another program's categories.
func internCategories(in *stringInterner, sets map[string][]string, cats []string) []string {
	if len(cats) == 0 {
		return nil
	}
	var key string
	if len(cats) == 1 {
		key = cats[0]
	} else {
		n := 0
		for _, c := range cats {
			n += len(c) + 1
		}
		b := make([]byte, 0, n)
		for _, c := range cats {
			b = append(b, c...)
			b = append(b, 0)
		}
		key = string(b)
	}
	if shared, ok := sets[key]; ok {
		return shared
	}
	shared := in.internSlice(cats)
	shared = shared[:len(shared):len(shared)]
	sets[key] = shared
	return shared
}

type stringInterner struct {
	m map[string]string
}

func newStringInterner() *stringInterner {
	return &stringInterner{m: make(map[string]string)}
}

func (in *stringInterner) intern(s string) string {
	if s == "" {
		return ""
	}
	if v, ok := in.m[s]; ok {
		return v
	}
	in.m[s] = s
	return s
}

func (in *stringInterner) internSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = in.intern(v)
	}
	return out
}

// releaseGuideMemory returns the memory freed by a guide swap to the OS.
// Replacing a guide frees hundreds of MB at once; without this the Go
// runtime holds it for minutes, inflating RSS. Guide swaps are rare (startup
// restore and scheduled refreshes), so the forced GC is cheap overall.
func releaseGuideMemory() {
	debug.FreeOSMemory()
}
