package screenshot

import (
	"regexp"
	"strings"
)

// sanitizeFilename sanitizes a filename by removing illegal characters
func sanitizeFilename(filename string) string {
	// Replace illegal characters with underscore
	re := regexp.MustCompile(`[\\/:*?"<>|]`)
	sanitized := re.ReplaceAllString(filename, "_")

	// Replace spaces with underscores
	sanitized = strings.ReplaceAll(sanitized, " ", "_")

	// Limit length to avoid issues with long filenames
	if len(sanitized) > 100 {
		sanitized = sanitized[:100]
	}

	return sanitized
}
