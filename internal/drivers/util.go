package drivers

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
