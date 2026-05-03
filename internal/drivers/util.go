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

// boolFromConfig extracts a boolean value from a config map. Returns
// defaultVal when the key is absent or holds a non-bool value. structpb
// preserves bool natively so no float64 fallback is needed here.
func boolFromConfig(config map[string]any, key string, defaultVal bool) bool {
	if v, ok := config[key].(bool); ok {
		return v
	}
	return defaultVal
}

// strSliceFromConfig extracts a []string from a config map. Accepts either
// the typed []string shape (uncommon outside Go-native callers) or the
// []any shape that survives a structpb round-trip (the common case for
// values that originate in YAML/JSON). Non-string entries and empty
// strings are dropped silently — callers needing strict validation should
// re-check the result.
func strSliceFromConfig(config map[string]any, key string) []string {
	v, ok := config[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []string:
		out := make([]string, 0, len(t))
		for _, s := range t {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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
