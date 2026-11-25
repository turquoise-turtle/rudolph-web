package main

// A function deployed before the minimum Santa version became Rudolph's stored setting may still have
// RUDOLPH_WEB_MIN_SANTA_VERSION in its environment. It is ignored, and the entry point logs each line
// config.IgnoredEnvironment returns once at start-up, so an operator who set it finds out where it went (owner
// decision 3). The entry point no longer passes a minimum to the web handler: the handler reads Rudolph's setting.

import (
	"os"
	"strings"
	"testing"
)

func TestStartUpLogsIgnoredEnvironment(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "config.IgnoredEnvironment(") {
		t.Error("main.go does not log config.IgnoredEnvironment's lines at start-up")
	}
	if strings.Contains(string(src), "MinSantaVersion") {
		t.Error("main.go still passes a minimum Santa version of its own to the web handler")
	}
}
