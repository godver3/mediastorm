package handlers

import (
	"testing"
	"time"

	"novastream/models"
)

func TestApplyOffsetToProgram(t *testing.T) {
	start := time.Date(2025, 1, 15, 20, 0, 0, 0, time.UTC)
	stop := time.Date(2025, 1, 15, 21, 0, 0, 0, time.UTC)

	prog := models.EPGProgram{
		ChannelID: "ch1",
		Title:     "Test Show",
		Start:     start,
		Stop:      stop,
	}

	// Positive offset: shift forward 30 minutes
	shifted := applyOffsetToProgram(prog, 30*time.Minute)
	if !shifted.Start.Equal(start.Add(30 * time.Minute)) {
		t.Errorf("Start = %v, want %v", shifted.Start, start.Add(30*time.Minute))
	}
	if !shifted.Stop.Equal(stop.Add(30 * time.Minute)) {
		t.Errorf("Stop = %v, want %v", shifted.Stop, stop.Add(30*time.Minute))
	}

	// Negative offset: shift backward 2 hours
	shifted = applyOffsetToProgram(prog, -2*time.Hour)
	if !shifted.Start.Equal(start.Add(-2 * time.Hour)) {
		t.Errorf("Start = %v, want %v", shifted.Start, start.Add(-2*time.Hour))
	}
	if !shifted.Stop.Equal(stop.Add(-2 * time.Hour)) {
		t.Errorf("Stop = %v, want %v", shifted.Stop, stop.Add(-2*time.Hour))
	}

	// Zero offset: no change
	shifted = applyOffsetToProgram(prog, 0)
	if !shifted.Start.Equal(start) {
		t.Errorf("Start = %v, want %v (unchanged)", shifted.Start, start)
	}

	// Original should be unmodified
	if !prog.Start.Equal(start) {
		t.Error("Original program was modified")
	}
}
