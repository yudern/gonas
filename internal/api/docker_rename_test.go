package api

import "testing"

func TestValidContainerName(t *testing.T) {
	ok := []string{"nginx", "my-app_1", "App.2", "a"}
	for _, s := range ok {
		if !validContainerName(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	bad := []string{"", "-leading", ".dot", "has space", "bad/slash", "日本語"}
	for _, s := range bad {
		if validContainerName(s) {
			t.Errorf("%q should be rejected", s)
		}
	}
}
