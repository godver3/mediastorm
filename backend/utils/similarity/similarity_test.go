package similarity

import (
	"testing"
)

func TestSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		s1       string
		s2       string
		minScore float64 // minimum acceptable similarity score
	}{
		{
			name:     "Identical strings",
			s1:       "The Matrix",
			s2:       "The Matrix",
			minScore: 1.0,
		},
		{
			name:     "Case insensitive",
			s1:       "The Matrix",
			s2:       "the matrix",
			minScore: 1.0,
		},
		{
			name:     "With dots vs spaces",
			s1:       "The.Matrix",
			s2:       "The Matrix",
			minScore: 0.9,
		},
		{
			name:     "Year in one string",
			s1:       "The Matrix 1999",
			s2:       "The Matrix",
			minScore: 0.65,
		},
		{
			name:     "Different strings",
			s1:       "The Matrix",
			s2:       "Inception",
			minScore: 0.0,
		},
		{
			name:     "Similar movie titles",
			s1:       "The Dark Knight",
			s2:       "Dark Knight",
			minScore: 0.7,
		},
		{
			name:     "Release name vs clean title",
			s1:       "The.Matrix.1999.1080p.BluRay.x264",
			s2:       "The Matrix",
			minScore: 0.3, // Will be lower due to extra info
		},
		{
			name:     "Ampersand vs and",
			s1:       "Me, MYSELF & I",
			s2:       "Me Myself and I",
			minScore: 1.0, // Should be identical after normalization
		},
		{
			name:     "Ampersand in title",
			s1:       "Law & Order",
			s2:       "Law and Order",
			minScore: 1.0,
		},
		{
			name:     "Diacritics stripped - French accents",
			s1:       "Hors Reseau L Experience",
			s2:       "Hors réseau : L'expérience",
			minScore: 0.95,
		},
		{
			name:     "Diacritics stripped - German umlauts",
			s1:       "Uber",
			s2:       "Über",
			minScore: 1.0,
		},
		{
			name:     "Diacritics stripped - Spanish tildes",
			s1:       "Espana",
			s2:       "España",
			minScore: 1.0,
		},
		{
			name:     "Diacritics stripped - mixed accents",
			s1:       "Amelie",
			s2:       "Amélie",
			minScore: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := Similarity(tt.s1, tt.s2)
			t.Logf("Similarity(%q, %q) = %.2f", tt.s1, tt.s2, score)

			if tt.minScore == 1.0 && score != 1.0 {
				t.Errorf("Expected exact match (1.0), got %.2f", score)
			} else if score < tt.minScore {
				t.Errorf("Expected score >= %.2f, got %.2f", tt.minScore, score)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"The Matrix", "the matrix"},
		{"The.Matrix", "the matrix"},
		{"The-Matrix", "the matrix"},
		{"The_Matrix", "the matrix"},
		{"The   Matrix", "the matrix"},
		{"The Matrix 1999", "the matrix 1999"},
		{"The Matrix (1999)", "the matrix 1999"},
		{"Law & Order", "law and order"},
		{"Me, MYSELF & I", "me myself and i"},
		{"Rock & Roll", "rock and roll"},
		{"Hors réseau : L'expérience", "hors reseau lexperience"},
		{"Amélie", "amelie"},
		{"Über", "uber"},
		{"España", "espana"},
		{"café crème", "cafe creme"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalize(tt.input)
			if result != tt.expected {
				t.Errorf("normalize(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
