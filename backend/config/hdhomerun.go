package config

import (
	"encoding/hex"
	"errors"
	"net/mail"
	"net/url"
	"sort"
	"strings"
)

// NormalizeHDHomeRunGuideAuth validates the account-based XMLTV method, which
// is required for the full guide (automatic DeviceAuth only returns a limited one).
// Errors intentionally exclude account identifiers so they are safe to log.
func NormalizeHDHomeRunGuideAuth(email, deviceIDs string) (string, string, error) {
	email = strings.TrimSpace(email)
	deviceIDs = strings.TrimSpace(deviceIDs)
	if email == "" && deviceIDs == "" {
		return "", "", nil
	}
	if email == "" || deviceIDs == "" {
		return "", "", errors.New("HDHomeRun guide email and device IDs must both be provided for the full guide (leave both empty for the limited automatic guide)")
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", "", errors.New("HDHomeRun guide email must be a valid email address")
	}
	seen := make(map[string]bool)
	var ids []string
	for _, raw := range strings.Split(deviceIDs, ",") {
		id := strings.ToUpper(strings.TrimSpace(raw))
		if _, err := hex.DecodeString(id); len(id) != 8 || err != nil {
			return "", "", errors.New("HDHomeRun guide device IDs must be comma-separated 8-character hexadecimal IDs from the tuner web UI")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	sort.Strings(ids)
	return email, strings.Join(ids, ","), nil
}

// HDHomeRunURL accepts a tuner IP/hostname or base URL. Only the device's
// documented endpoints are constructed; credentials and arbitrary paths are invalid.
func HDHomeRunURL(address, endpoint string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("HDHomeRun tuner address is required")
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" ||
		(u.Path != "" && u.Path != "/" && u.Path != "/lineup.m3u") {
		return "", errors.New("HDHomeRun address must be an IP, hostname, or HTTP base URL")
	}
	u.Path = endpoint
	return u.String(), nil
}
