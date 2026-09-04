package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/screenshot"
)

func TestScreenshotRenderer_SkipsPrivateURL(t *testing.T) {
	s := screenshotRenderer{p: screenshot.NewPool()}
	_, err := s.ExtractMetadata(context.Background(), "http://127.0.0.1/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private target")
}

func TestScreenshotRenderer_SkipsIMDS(t *testing.T) {
	s := screenshotRenderer{p: screenshot.NewPool()}
	_, err := s.ExtractMetadata(context.Background(), "http://169.254.169.254/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private target")
}
