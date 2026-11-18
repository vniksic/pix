package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseAge(t *testing.T) {
	day := 24 * time.Hour
	for s, want := range map[string]time.Duration{
		"90m":    90 * time.Minute,
		"12h":    12 * time.Hour,
		"1d":     day,
		"2w":     14 * day,
		"01d":    day,
		"15250w": 15250 * 7 * day, // about 292 years, still a time.Duration
	} {
		if got, err := parseAge(s); err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{
		"", "d", "1", "0d", "00h", "-1d", "+1d", "1.5d", "1 d", " 1d", "1d ",
		"1D", "1M", "1s", "1y", "1dd", "1d2h", "1h30m", "abc", "1e3m", "0x10m", "１d",
	} {
		if got, err := parseAge(s); err == nil || !strings.Contains(err.Error(), "whole number") {
			t.Errorf("parseAge(%q) = %v, %v; want the format error", s, got, err)
		}
	}
	for _, s := range []string{"15251w", "106752d", "9223372036854775807m", "99999999999999999999w"} {
		if got, err := parseAge(s); err == nil || !strings.Contains(err.Error(), "too far") {
			t.Errorf("parseAge(%q) = %v, %v; want the range error", s, got, err)
		}
	}
}
