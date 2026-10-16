package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/xfey/coconet/packages/conversation"
)

// Content is stored in the configured artifact backend. Metadata holds only
// immutable manifests and object identities, never conversation block bodies.
type artifactWriter interface {
	Write(context.Context, artifactObject, []byte) error
}

func (s *s3ArtifactStore) Write(ctx context.Context, object artifactObject, data []byte) error {
	if !validArtifactObjectKey(object.Key) || int64(len(data)) != object.Size || conversation.Digest(data) != object.SHA256 {
		return errors.New("invalid content object")
	}
	options := minio.PutObjectOptions{ContentType: "application/octet-stream"}
	options.SetMatchETagExcept("*")
	_, err := s.client.PutObject(ctx, s.bucket, object.Key, bytes.NewReader(data), object.Size, options)
	if err == nil {
		return nil
	}
	if minio.ToErrorResponse(err).StatusCode != 412 {
		return err
	}
	reader, err := s.Open(ctx, object.Key)
	if err != nil {
		return err
	}
	stored, readErr := io.ReadAll(io.LimitReader(reader, object.Size+1))
	if err := errors.Join(readErr, reader.Close()); err != nil {
		return err
	}
	if int64(len(stored)) != object.Size || conversation.Digest(stored) != object.SHA256 {
		return errors.New("immutable content conflict")
	}
	return nil
}

func (s *filesystemArtifactStore) Write(ctx context.Context, object artifactObject, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validArtifactObjectKey(object.Key) || int64(len(data)) != object.Size || conversation.Digest(data) != object.SHA256 {
		return errors.New("invalid content object")
	}
	if err := s.ensureDirectories(); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.objectsPath, ".content-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.objectPath(object.Key)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		matches, err := s.existingObjectMatches(artifactCapability{ObjectKey: object.Key, SHA256: object.SHA256, Size: object.Size})
		if err != nil {
			return err
		}
		if !matches {
			return errors.New("immutable content conflict")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(s.objectsPath)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

type contentObjectRecord struct {
	ProjectID string `json:"project_id"`
	SHA256    string `json:"sha256"`
	Size      int    `json:"size"`
	ObjectKey string `json:"object_key"`
	Ready     bool   `json:"ready"`
	HoldUntil string `json:"hold_until"`
}

func contentObjectKey(project, hash string) string { return project + ":" + hash }

func readConversation(state databaseState, project string, manifest conversation.Manifest, start, end int) ([]conversation.Entry, error) {
	return conversation.Read(manifest, start, end, func(block conversation.Block) ([]byte, error) {
		object, ok := state.ContentObjects.Lookup(contentObjectKey(project, block.SHA256))
		if !ok || !object.Ready || object.Size != block.Size || state.contents == nil {
			return nil, errors.New("conversation content is unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		reader, err := state.contents.Open(ctx, object.ObjectKey)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(reader, int64(block.Size)+1))
		return data, errors.Join(err, reader.Close())
	})
}
