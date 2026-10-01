package docker

import (
	"context"
	"net/http"
	"testing"
)

func TestRemoveImage_SendsDelete(t *testing.T) {
	var gotPath, gotMethod string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		_, _ = w.Write([]byte(`[{"Deleted":"sha256:abc"}]`))
	})
	if err := c.RemoveImage(context.Background(), "sha256:abc", false); err != nil {
		t.Fatalf("RemoveImage error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", gotMethod)
	}
	if gotPath != "/images/sha256:abc" {
		t.Errorf("unexpected path %q", gotPath)
	}
}

func TestRemoveImage_InUseSurfacesError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"image is being used by running container"}`))
	})
	if err := c.RemoveImage(context.Background(), "sha256:abc", false); err == nil {
		t.Fatal("expected an error when the image is in use")
	}
}

func TestPruneImages_ReturnsReclaimed(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/prune" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ImagesDeleted":[],"SpaceReclaimed":1048576}`))
	})
	res, err := c.PruneImages(context.Background())
	if err != nil {
		t.Fatalf("PruneImages error: %v", err)
	}
	if res.SpaceReclaimed != 1048576 {
		t.Errorf("SpaceReclaimed = %d, want 1048576", res.SpaceReclaimed)
	}
}
