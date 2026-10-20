package main

import "os"

// env reads a setting from EGO_<key>.
func env(key string) string {
	return os.Getenv("EGO_" + key)
}
