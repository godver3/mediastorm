package models

import (
	"strconv"
	"strings"
)

// ProgrammeMinimumAge accepts explicit German FSK/FSF minimum ages. An
// unlabelled number is ambiguous across rating systems and remains unknown.
func ProgrammeMinimumAge(program EPGProgram) (int, bool) {
	ratings := program.Ratings
	if len(ratings) == 0 && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(program.Rating)), "FSK") {
		ratings = []EPGRating{{System: "FSK", Value: program.Rating}}
	}
	age, known := 0, false
	for _, rating := range ratings {
		system := strings.ToUpper(strings.TrimSpace(rating.System))
		if system != "FSK" && system != "FSF" && system != "DE-FSK" {
			continue
		}
		value := strings.ToUpper(strings.TrimSpace(rating.Value))
		value = strings.TrimSpace(strings.TrimPrefix(value, "FSK"))
		value = strings.TrimSpace(strings.TrimPrefix(value, "AB"))
		value = strings.TrimSpace(strings.TrimSuffix(value, "+"))
		n, err := strconv.Atoi(value)
		if err != nil || (n != 0 && n != 6 && n != 12 && n != 16 && n != 18) {
			return 0, false
		}
		if n > age {
			age = n
		}
		known = true
	}
	return age, known
}
