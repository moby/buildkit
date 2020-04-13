package agent

import "os"

func addToMapIfEmpty(dest map[string]interface{}, source map[string]interface{}) {
	if source == nil {
		return
	}
	for k, newValue := range source {
		if oldValue, ok := dest[k]; !ok || oldValue == "" {
			dest[k] = newValue
		}
	}
}

func addElementToMapIfEmpty(source map[string]interface{}, key string, value interface{}) {
	if val, ok := source[key]; !ok || val == "" {
		source[key] = value
	}
}

func getSourceRootFromEnv(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		// We check if is a valid and existing folder
		if fInfo, err := os.Stat(value); err == nil && fInfo.IsDir() {
			return value
		}
	}
	return ""
}
