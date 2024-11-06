// SPDX-FileCopyrightText: 2024 Josef Andersson
//
// SPDX-License-Identifier: EUPL-1.2

// Package target handles operations related to archiving git repositories.
package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/mholt/archiver/v4"

	"itiquette/git-provider-sync/internal/interfaces"
	"itiquette/git-provider-sync/internal/log"
	"itiquette/git-provider-sync/internal/model"
	gpsconfig "itiquette/git-provider-sync/internal/model/configuration"
)

var (
	ErrArchiveCompression = errors.New("failed to compress archive")
	ErrArchiveCreation    = errors.New("failed to create archive file")
	ErrDirectoryCreation  = errors.New("failed to create target directory")
	ErrNoFilesToArchive   = errors.New("no files found to archive")
)

// Archive represents a structure capable of pushing Git repositories to archive files.
type Archive struct {
	gitClient *gitLib
}

// Push initializes a target repository and creates an archive of it.
func (a *Archive) Push(ctx context.Context, repo interfaces.GitRepository, opt model.PushOption, _ gpsconfig.GitOption) error {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Archive:Push")
	opt.DebugLog(logger).Msg("Archive:Push")

	sourceDir, err := a.initializeTargetRepository(ctx, repo, opt)
	if err != nil {
		return fmt.Errorf("failed to initialize target repository: %w", err)
	}

	return createArchive(ctx, sourceDir, opt.Target, repo.ProjectInfo().Name(ctx))
}

func (a *Archive) initializeTargetRepository(ctx context.Context, repo interfaces.GitRepository, opt model.PushOption) (string, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Archive:initializeTargetRepository")

	sourceDir, err := getSourceDirPath(ctx, opt)
	if err != nil {
		return "", err
	}

	initializedRepo, err := git.PlainInit(sourceDir, false)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRepoInitialization, err)
	}

	pushOpt := model.NewPushOption(sourceDir, false, true, gpsconfig.HTTPClientOption{})
	if err := a.gitClient.Push(ctx, repo, pushOpt, gpsconfig.GitOption{}); err != nil {
		return "", fmt.Errorf("%w: %w", ErrPushRepository, err)
	}

	if err := setRemoteAndBranch(ctx, repo, sourceDir); err != nil {
		return "", err
	}

	if err := a.gitClient.gitLibOperation.SetDefaultBranch(ctx, initializedRepo, repo.ProjectInfo().DefaultBranch); err != nil {
		return "", err //nolint
	}

	return sourceDir, nil
}

func createArchive(ctx context.Context, sourceDir, targetArchive, name string) error {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Archive:createArchive")

	files, err := mapFilesToArchive(ctx, sourceDir, name)
	if err != nil {
		return err
	}

	return compress(ctx, targetArchive, files)
}

func compress(ctx context.Context, targetPath string, files []archiver.File) error {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Archive:compress")

	file, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrArchiveCreation, targetPath, err)
	}
	defer file.Close()

	if err := os.Chmod(targetPath, 0o644); err != nil {
		return fmt.Errorf("failed to set permissions on %s: %w", targetPath, err)
	}

	format := archiver.CompressedArchive{
		Compression: archiver.Gz{},
		Archival:    archiver.Tar{},
	}

	if err := format.Archive(ctx, file, files); err != nil {
		return fmt.Errorf("%w: %w", ErrArchiveCompression, err)
	}

	return nil
}

func mapFilesToArchive(ctx context.Context, sourceDir, targetName string) ([]archiver.File, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Archive:mapFilesToArchive")

	files, err := archiver.FilesFromDisk(nil, map[string]string{
		sourceDir: targetName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to map files at %s to tar archive: %w", sourceDir, err)
	}

	if len(files) <= 1 {
		return nil, fmt.Errorf("%w: %s", ErrNoFilesToArchive, sourceDir)
	}

	return files, nil
}

func NewArchive() *Archive {
	return &Archive{gitClient: NewGitLib()}
}

func getSourceDirPath(ctx context.Context, opt model.PushOption) (string, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Archive:getSourceDirPath")

	sourceDir := strings.TrimSuffix(opt.Target, ".tar.gz")
	if err := os.MkdirAll(filepath.Dir(sourceDir), os.ModePerm); err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrDirectoryCreation, filepath.Dir(sourceDir), err)
	}

	return sourceDir, nil
}

// ArchiveTargetPath generates the full path for the target archive file.
func ArchiveTargetPath(name, targetDir string) string {
	tarArchive := fmt.Sprintf("%s%s.tar.gz", name, nowString())

	return filepath.Join(targetDir, tarArchive)
}

// nowString returns a string representation of the current time.
// The format is _yearmonthday_hourminutesecond_unixmilli.
// This is used to create unique timestamps for archive file names.
func nowString() string {
	now := time.Now()

	return fmt.Sprintf("_%d%02d%02d_%02d%02d%02d_%d",
		now.Year(), now.Month(), now.Day(),
		now.Hour(), now.Minute(), now.Second(), now.UnixMilli())
}
