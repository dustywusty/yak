package main

import (
	"os"
	"strings"
)

// yakEnv prefers a nonempty YAK value, then its transition ABBY alias.
func yakEnv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if strings.HasPrefix(key, "YAK_") {
		return os.Getenv("ABBY_" + strings.TrimPrefix(key, "YAK_"))
	}
	return ""
}
