package models

import "testing"

func TestProgrammeMinimumAgeRequiresKnownSystem(t *testing.T) {
	for _, tc := range []struct {
		name, system, value, legacy string
		age                         int
		known                       bool
	}{
		{"FSK 0", "FSK", "0", "", 0, true}, {"FSK 6", "FSK", "ab 6", "", 6, true}, {"FSK 12", "DE-FSK", "FSK 12", "", 12, true},
		{"FSF 16", "FSF", "16+", "", 16, true}, {"FSK 18", "FSK", "18", "", 18, true},
		{"unlabelled number", "", "6", "", 0, false}, {"BBFC number", "BBFC", "12", "", 0, false},
		{"invalid German rating", "FSK", "7", "", 0, false}, {"missing", "", "", "", 0, false},
		{"legacy explicit FSK", "", "", "FSK 6", 6, true}, {"legacy ambiguous", "", "", "6", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program := EPGProgram{Rating: tc.legacy}
			if tc.value != "" {
				program.Ratings = []EPGRating{{System: tc.system, Value: tc.value}}
			}
			age, known := ProgrammeMinimumAge(program)
			if age != tc.age || known != tc.known {
				t.Fatalf("got %d %v, want %d %v", age, known, tc.age, tc.known)
			}
		})
	}
	age, known := ProgrammeMinimumAge(EPGProgram{Ratings: []EPGRating{{System: "FSK", Value: "6"}, {System: "FSK", Value: "16"}}})
	if !known || age != 16 {
		t.Fatal("conflicting minimum ages must use the higher limit")
	}
}
