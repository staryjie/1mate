package store

import (
	"encoding/json"
	"strconv"
)

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intsJSON(v []int) string {
	if v == nil {
		return "[]"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func parseIntsJSON(s string) []int {
	var out []int
	if s == "" {
		return []int{}
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []int{}
	}
	if out == nil {
		return []int{}
	}
	return out
}

// atoiSafe is used by parsing code that must never panic on odd input.
func atoiSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
