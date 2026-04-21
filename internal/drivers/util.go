package drivers

import "strings"

// intFromConfig extracts an integer value from a config map, returning a default if absent.
func intFromConfig(config map[string]any, key string, defaultVal int) (int, bool) {
	v, ok := config[key]
	if !ok {
		return defaultVal, false
	}
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	}
	return defaultVal, false
}

// strFromConfig extracts a string value from a config map with a default.
func strFromConfig(config map[string]any, key, defaultVal string) string {
	if v, ok := config[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}

// imageRepo returns the repository portion of a flat "<repo>:<tag>" image reference.
func imageRepo(image string) string {
	parts := splitImageRef(image)
	return parts[0]
}

// imageTag returns the tag portion of a flat "<repo>:<tag>" image reference, defaulting to "latest".
func imageTag(image string) string {
	parts := splitImageRef(image)
	if len(parts) == 2 {
		return parts[1]
	}
	return "latest"
}

// splitImageRef splits an image reference on the last colon that follows a slash
// (i.e. the tag delimiter), returning [ref] or [ref, tag].
func splitImageRef(image string) []string {
	// Find the last slash to anchor the colon search to the tag portion only.
	lastSlash := strings.LastIndex(image, "/")
	tagIdx := strings.Index(image[lastSlash+1:], ":")
	if tagIdx < 0 {
		return []string{image}
	}
	split := lastSlash + 1 + tagIdx
	return []string{image[:split], image[split+1:]}
}
