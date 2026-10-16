package main

import (
	"context"
	"errors"
	"os"

	"github.com/minio/minio-go/v7"
)

type artifactDeletionStore interface {
	Delete(context.Context, string) error
}

func (store *s3ArtifactStore) Delete(ctx context.Context, key string) error {
	if !validArtifactObjectKey(key) {
		return errors.New("invalid artifact deletion key")
	}
	return store.client.RemoveObject(ctx, store.bucket, key, minio.RemoveObjectOptions{})
}

func (store *filesystemArtifactStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.ensureDirectories(); err != nil {
		return err
	}
	path, err := store.objectPath(key)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("artifact deletion requires a regular file")
	}
	return os.Remove(path)
}

var errNoArtifactGarbageChange = errors.New("artifact cleanup has no metadata changes")
