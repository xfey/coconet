package contentstore

import (
	"bytes"
	"io"
	"testing"
)

func TestStreamingUploadVerifiesLengthAndDigest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("source text\n"), 10000)
	digest := ObjectDigest(data)
	for _, body := range [][]byte{data[:len(data)-1], append(append([]byte{}, data...), 1), []byte("wrong")} {
		if err := s.PutStream(bytes.NewReader(body), digest, int64(len(data))); err == nil {
			t.Fatal("invalid stream published")
		}
	}
	if err := s.PutStream(bytes.NewReader(data), digest, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	f, err := s.OpenObject(digest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("streaming content changed")
	}
}

func TestUnknownLengthDownloadDoesNotPublishWrongBytes(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("downloaded original source")
	digest := ObjectDigest(data)
	if err := s.Receive(bytes.NewReader([]byte("corrupt")), digest); err == nil {
		t.Fatal("corrupt download accepted")
	}
	if _, err := s.OpenObject(digest); err == nil {
		t.Fatal("failed download was published")
	}
	if err := s.Receive(bytes.NewReader(data), digest); err != nil {
		t.Fatal(err)
	}
	got, err := s.Object(digest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("download changed content")
	}
}
