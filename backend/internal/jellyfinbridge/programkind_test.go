package jellyfinbridge

import (
	"testing"
	"time"

	"novastream/models"
)

func TestClassifyProgram(t *testing.T) {
	at := time.Date(2026, 11, 19, 20, 15, 0, 0, time.UTC)
	prog := func(title string, minutes int, cats ...string) models.EPGProgram {
		return models.EPGProgram{Title: title, Start: at, Stop: at.Add(time.Duration(minutes) * time.Minute), Categories: cats}
	}
	for _, tc := range []struct {
		name string
		prog models.EPGProgram
		want programKind
	}{
		{"crime episode", prog("Morden im Norden (96)", 45, "Krimi"), programKind{series: true}},
		{"crime film", prog("Tatort: Das Haus", 90, "Krimi"), programKind{movie: true}},
		{"thriller film", prog("Ein Thriller", 110, "Thriller", "Action"), programKind{movie: true}},
		{"sitcom", prog("Two and a Half Men", 25, "Sitcom", "Comedy"), programKind{series: true}},
		{"news", prog("Tagesschau", 15, "Nachrichten"), programKind{news: true}},
		{"football", prog("Bundesliga", 120, "Fußball"), programKind{sports: true}},
		{"kids cartoon", prog("Paw Patrol", 25, "Kinder", "Animation"), programKind{kids: true, series: true}},
		{"documentary", prog("Terra X", 45, "Dokumentation", "Wissen"), programKind{}},
		{"teleshopping", prog("Teleshopping", 240, "Verschiedenes"), programKind{}},
		{"english movie", prog("Some Film", 100, "Movie"), programKind{movie: true}},
		{"episode number", models.EPGProgram{Title: "Show", Episode: "S01E02", Start: at, Stop: at.Add(time.Hour)}, programKind{series: true}},
	} {
		if got := classifyProgram(tc.prog); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}

	yes, no := true, false
	series := programKind{series: true}
	// The Live TV page's series row: IsSeries and none of the others.
	if !series.matches(programQueryParams{IsSeries: &yes, IsMovie: &no, IsSports: &no, IsKids: &no, IsNews: &no}) {
		t.Fatal("series rejected by the series row")
	}
	if (programKind{}).matches(programQueryParams{IsSeries: &yes}) || series.matches(programQueryParams{IsMovie: &yes}) {
		t.Fatal("kind filter let a programme through")
	}
}
