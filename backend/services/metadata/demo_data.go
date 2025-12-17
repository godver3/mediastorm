package metadata

import "novastream/models"

var demoTrendingMovies = []models.TrendingItem{
	{
		Rank: 1,
		Title: models.Title{
			ID:        "tvdb:movie:1831",
			Name:      "Night of the Living Dead",
			Overview:  "A group of survivors barricade themselves in a farmhouse as the dead return to life and hunger for the living.",
			Year:      1968,
			Language:  "en",
			MediaType: "movie",
			TVDBID:    1831,
			IMDBID:    "tt0063350",
		},
	},
	{
		Rank: 2,
		Title: models.Title{
			ID:        "tvdb:movie:20376",
			Name:      "The Brain That Wouldn't Die",
			Overview:  "A scientist keeps his fiancée's severed head alive while searching for a new body to complete his experiment.",
			Year:      1962,
			Language:  "en",
			MediaType: "movie",
			TVDBID:    20376,
			IMDBID:    "tt0052646",
		},
	},
	{
		Rank: 3,
		Title: models.Title{
			ID:        "tvdb:movie:9984",
			Name:      "Detour",
			Overview:  "A down-on-his-luck musician hitchhiking to Hollywood gets caught up in a web of fate and murder.",
			Year:      1945,
			Language:  "en",
			MediaType: "movie",
			TVDBID:    9984,
			IMDBID:    "tt0037638",
		},
	},
}

var demoTrendingSeries = []models.TrendingItem{
	{
		Rank: 1,
		Title: models.Title{
			ID:        "tvdb:series:71471",
			Name:      "The Beverly Hillbillies",
			Overview:  "A poor backwoods family strikes oil and moves to Beverly Hills, where their down-home ways clash hilariously with high society.",
			Year:      1962,
			Language:  "en",
			MediaType: "tv",
			TVDBID:    71471,
			IMDBID:    "tt0055662",
		},
	},
	{
		Rank: 2,
		Title: models.Title{
			ID:        "tvdb:series:76479",
			Name:      "One Step Beyond",
			Overview:  "Hosted by John Newland, this anthology explores allegedly true tales of the paranormal and unexplained.",
			Year:      1959,
			Language:  "en",
			MediaType: "tv",
			TVDBID:    76479,
			IMDBID:    "tt0052442",
		},
	},
	{
		Rank: 3,
		Title: models.Title{
			ID:        "tvdb:series:77404",
			Name:      "The Cisco Kid",
			Overview:  "The charming Mexican caballero and his sidekick Pancho ride through the Old West helping those in need.",
			Year:      1950,
			Language:  "en",
			MediaType: "tv",
			TVDBID:    77404,
			IMDBID:    "tt0042093",
		},
	},
}

func selectDemoTrending(mediaType string) []models.TrendingItem {
	if mediaType == "movie" {
		return demoTrendingMovies
	}
	return demoTrendingSeries
}

func copyTrendingItems(items []models.TrendingItem) []models.TrendingItem {
	cloned := make([]models.TrendingItem, len(items))
	copy(cloned, items)
	return cloned
}
