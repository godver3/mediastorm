package handlers

import (
	"novastream/models"
	"regexp"
	"strconv"
	"strings"
)

var sportsResolutionLabel = regexp.MustCompile(`(?i)\b(4320[pi]|2160[pi]|1080[pi]|720[pi]|480[pi]|8k|4k|uhd|fhd)\b`)
var sportsBitrateLabel = regexp.MustCompile(`(?i)\b([0-9]+(?:\.[0-9]+)?)\s*(mbps|kbps)\b`)

// Only explicit quality labels are interpreted. No stream/network probing occurs.
// Conflicting labels use the lower advertised resolution rather than overclaiming.
func reportedSportsQuality(label string) *models.SportsReportedQuality {
	height := 0
	for _, token := range sportsResolutionLabel.FindAllString(strings.ToLower(label), -1) {
		value := 0
		switch token {
		case "8k":
			value = 4320
		case "4k", "uhd":
			value = 2160
		case "fhd":
			value = 1080
		default:
			value, _ = strconv.Atoi(strings.TrimRight(token, "pi"))
		}
		if height == 0 || value < height {
			height = value
		}
	}
	var bitrate int64
	for _, match := range sportsBitrateLabel.FindAllStringSubmatch(label, -1) {
		value, _ := strconv.ParseFloat(match[1], 64)
		factor := float64(1000)
		if strings.EqualFold(match[2], "mbps") {
			factor = 1000000
		}
		b := int64(value * factor)
		if b >= 10000 && b <= 1000000000 && (bitrate == 0 || b < bitrate) {
			bitrate = b
		}
	}
	if height == 0 && bitrate == 0 {
		return nil
	}
	return &models.SportsReportedQuality{ResolutionHeight: height, BitrateBps: bitrate, Origin: "label"}
}

// Known values precede unknown values, giving sorting a deterministic total order.
func compareSportsQuality(a, b *models.SportsReportedQuality) int {
	var ah, bh int
	var ab, bb int64
	if a != nil {
		ah = a.ResolutionHeight
		ab = a.BitrateBps
	}
	if b != nil {
		bh = b.ResolutionHeight
		bb = b.BitrateBps
	}
	if ah > bh {
		return -1
	}
	if ah < bh {
		return 1
	}
	if ab > bb {
		return -1
	}
	if ab < bb {
		return 1
	}
	return 0
}
