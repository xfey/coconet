package contentstore

import (
	"context"
	"testing"
)

func TestInventoryContainsOnlyValidatedMetadataAndHonorsCancellation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"one", "two", "three"} {
		if _, err := s.PutObject([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	objects, err := s.Inventory(context.Background())
	if err != nil || len(objects) != 3 {
		t.Fatal(objects, err)
	}
	var bytes int64
	for i, object := range objects {
		bytes += object.Size
		if object.Modified.IsZero() || i > 0 && object.SHA256 <= objects[i-1].SHA256 {
			t.Fatal(object)
		}
	}
	if bytes != 11 {
		t.Fatal(bytes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Inventory(ctx); err == nil {
		t.Fatal("cancelled inventory continued")
	}
}
