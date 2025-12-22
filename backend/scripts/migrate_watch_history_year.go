package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"novastream/models"
	"novastream/services/metadata"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: migrate_watch_history_year <cache_dir>")
	}

	cacheDir := os.Args[1]
	historyPath := filepath.Join(cacheDir, "watch_history.json")

	// Get API keys from environment variables
	tvdbAPIKey := os.Getenv("TVDB_API_KEY")
	tmdbAPIKey := os.Getenv("TMDB_API_KEY")

	if tvdbAPIKey == "" {
		log.Fatal("TVDB_API_KEY environment variable is required")
	}
	if tmdbAPIKey == "" {
		log.Fatal("TMDB_API_KEY environment variable is required")
	}

	// Read the watch history file
	data, err := os.ReadFile(historyPath)
	if err != nil {
		log.Fatalf("Failed to read watch history: %v", err)
	}

	var history map[string]map[string]models.SeriesWatchState
	if err := json.Unmarshal(data, &history); err != nil {
		log.Fatalf("Failed to parse watch history: %v", err)
	}

	// Initialize metadata service (it will use cacheDir/metadata subdirectory internally)
	metadataService := metadata.NewService(tvdbAPIKey, tmdbAPIKey, "en", cacheDir, 24, false)

	ctx := context.Background()
	updated := 0
	total := 0

	// Process each user's watch history
	for _, userHistory := range history {
		for seriesID, state := range userHistory {
			total++

			// Skip if year already exists
			if state.Year > 0 {
				log.Printf("Series %s already has year %d, skipping", seriesID, state.Year)
				continue
			}

			// Extract TMDB ID from seriesId
			var tmdbID int64
			if strings.HasPrefix(seriesID, "tmdb:tv:") {
				idStr := strings.TrimPrefix(seriesID, "tmdb:tv:")
				parsed, err := strconv.ParseInt(idStr, 10, 64)
				if err == nil {
					tmdbID = parsed
				}
			}

			if tmdbID <= 0 {
				log.Printf("Series %s has no TMDB ID, skipping year lookup", seriesID)
				continue
			}

			// Fetch series details to get the year
			log.Printf("Looking up year for series %s (TMDB: %d)...", state.SeriesTitle, tmdbID)
			details, err := metadataService.SeriesDetails(ctx, models.SeriesDetailsQuery{
				TitleID: seriesID,
				Name:    state.SeriesTitle,
				TVDBID:  0,
				Year:    0,
			})

			if err != nil {
				log.Printf("Warning: Failed to fetch details for %s: %v", seriesID, err)
				continue
			}

			if details.Title.Year > 0 {
				state.Year = details.Title.Year
				userHistory[seriesID] = state
				updated++
				log.Printf("✓ Updated %s with year %d", state.SeriesTitle, state.Year)
			} else {
				log.Printf("⚠ No year found for %s", state.SeriesTitle)
			}

			// Small delay to avoid rate limiting
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Write back the updated history
	if updated > 0 {
		backupPath := historyPath + ".backup-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backupPath, data, 0644); err != nil {
			log.Fatalf("Failed to create backup: %v", err)
		}
		log.Printf("Created backup at %s", backupPath)

		updatedData, err := json.MarshalIndent(history, "", "  ")
		if err != nil {
			log.Fatalf("Failed to marshal updated history: %v", err)
		}

		if err := os.WriteFile(historyPath, updatedData, 0644); err != nil {
			log.Fatalf("Failed to write updated history: %v", err)
		}

		log.Printf("\n✓ Migration complete: updated %d of %d series entries", updated, total)
	} else {
		log.Printf("\nNo updates needed (%d series already have year information)", total)
	}
}
