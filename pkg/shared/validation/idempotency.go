package validation

import "strings"

func IsIdempotencyKey(value string) bool {
	if len(value) < 16 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("-._:", character) {
			continue
		}
		return false
	}
	return true
}
