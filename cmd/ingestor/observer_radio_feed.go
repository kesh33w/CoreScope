package main

import (
	"encoding/hex"
	"strings"
)

// observerIDForMessage keeps legacy observer IDs unchanged. Explicit radio
// feeds share their real authenticated topic key but have separate database
// identities, so one feed cannot overwrite another feed's region or metrics.
// The suffix is a database identifier, never a signing/public key.
func observerIDForMessage(parts []string, msg map[string]interface{}) string {
	if len(parts) < 3 {
		return ""
	}
	id := clampObserverField(parts[2], maxObserverIDLen)
	radio, _ := msg["observer_radio_id"].(string)
	region := strings.ToUpper(parts[1])
	if len(id) != 64 || !observerFeedToken(radio, 32) || !observerFeedToken(region, maxObserverIATALen) {
		return id
	}
	if _, err := hex.DecodeString(id); err != nil {
		return id
	}
	return id + "@" + radio + "@" + region
}

func observerFeedToken(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
