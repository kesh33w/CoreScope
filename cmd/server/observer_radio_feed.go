package main

import "strings"

// observerPublicKey projects an explicitly scoped observer record onto its
// actual node identity. Observation filters keep the complete database ID;
// only node-key lookups and RF topology use this projection.
func observerPublicKey(id string) string {
	if len(id) < 68 || id[64] != '@' || !isHexAnyCase(id[:64]) {
		return id
	}
	parts := strings.Split(id[65:], "@")
	if len(parts) != 2 || !validObserverFeedPart(parts[0], 32) || !validObserverFeedPart(parts[1], 16) {
		return id
	}
	return id[:64]
}

func validObserverFeedPart(s string, max int) bool {
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
