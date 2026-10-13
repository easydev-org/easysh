package main

// trunc shortens a producer/consumer label for the graph table.
func trunc(s string) string {
	if len(s) > 21 {
		return s[:20] + "~"
	}
	return s
}
