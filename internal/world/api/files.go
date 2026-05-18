// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"time"

	"github.com/vaelen/wintermute/internal/files"
)

// FileAreaSpec is the input to CreateFileArea. Zero min-level fields
// fall back to the files-package defaults (read/write 1, admin 3).
type FileAreaSpec struct {
	Slug          string
	Name          string
	Description   string
	ReadMinLevel  int
	WriteMinLevel int
	AdminMinLevel int
}

// ListFilesOpts narrows ListFiles. OwnerUsername empty means "no owner
// filter"; Area empty means "no area filter".
type ListFilesOpts struct {
	OwnerUsername string
	Area          string
}

// CreateFileArea inserts a new file area row.
func (a *API) CreateFileArea(ctx context.Context, spec FileAreaSpec) error {
	if a.Files == nil {
		return errorf(CodeInternal, "files service not configured")
	}
	if spec.Slug == "" {
		return errorf(CodeInvalidArgument, "slug is required")
	}
	if spec.Name == "" {
		return errorf(CodeInvalidArgument, "name is required")
	}
	err := a.Files.CreateArea(ctx, files.Area{
		Slug:          spec.Slug,
		Name:          spec.Name,
		Description:   spec.Description,
		ReadMinLevel:  spec.ReadMinLevel,
		WriteMinLevel: spec.WriteMinLevel,
		AdminMinLevel: spec.AdminMinLevel,
	})
	return translateFilesErr("area", spec.Slug, err)
}

// DeleteFileArea removes a file area. Returns invalid_argument when any
// files still reference it, not_found if the area is unknown.
func (a *API) DeleteFileArea(ctx context.Context, slug string) error {
	if a.Files == nil {
		return errorf(CodeInternal, "files service not configured")
	}
	if slug == "" {
		return errorf(CodeInvalidArgument, "slug is required")
	}
	return translateFilesErr("area", slug, a.Files.DeleteArea(ctx, slug))
}

// ListFileAreas returns every file area, slug-ordered.
func (a *API) ListFileAreas(ctx context.Context) ([]files.Area, error) {
	if a.Files == nil {
		return nil, errorf(CodeInternal, "files service not configured")
	}
	out, err := a.Files.ListAreas(ctx)
	if err != nil {
		return nil, errorf(CodeInternal, "list areas: %v", err)
	}
	return out, nil
}

// ListFiles returns files matching opts, newest first.
func (a *API) ListFiles(ctx context.Context, opts ListFilesOpts) ([]files.File, error) {
	if a.Files == nil {
		return nil, errorf(CodeInternal, "files service not configured")
	}
	filter := files.ListFilter{Area: opts.Area}
	if opts.OwnerUsername != "" {
		id, err := a.accountIDByUsername(ctx, opts.OwnerUsername)
		if err != nil {
			return nil, err
		}
		filter.OwnerID = id
	}
	out, err := a.Files.ListFiles(ctx, filter)
	if err != nil {
		return nil, errorf(CodeInternal, "list files: %v", err)
	}
	return out, nil
}

// DeleteFile removes a file by slug. Returns not_found if no such row.
func (a *API) DeleteFile(ctx context.Context, slug string) error {
	if a.Files == nil {
		return errorf(CodeInternal, "files service not configured")
	}
	if slug == "" {
		return errorf(CodeInvalidArgument, "slug is required")
	}
	f, err := a.Files.GetFile(ctx, slug)
	if err != nil {
		return translateFilesErr("file", slug, err)
	}
	return translateFilesErr("file", slug, a.Files.DeleteFile(ctx, f.ID))
}

// SetFileACLs upserts one row in file_acls for (file_slug, account_username).
// perms = 0 removes the row.
func (a *API) SetFileACLs(ctx context.Context, fileSlug, username string, perms int) error {
	if a.Files == nil {
		return errorf(CodeInternal, "files service not configured")
	}
	if fileSlug == "" {
		return errorf(CodeInvalidArgument, "file slug is required")
	}
	if username == "" {
		return errorf(CodeInvalidArgument, "account_username is required")
	}
	f, err := a.Files.GetFile(ctx, fileSlug)
	if err != nil {
		return translateFilesErr("file", fileSlug, err)
	}
	id, err := a.accountIDByUsername(ctx, username)
	if err != nil {
		return err
	}
	if err := a.Files.SetACLs(ctx, f.ID, id, perms); err != nil {
		return errorf(CodeInternal, "set acls: %v", err)
	}
	return nil
}

// CleanupFiles runs the files janitor synchronously and returns the
// reaped counts.
func (a *API) CleanupFiles(ctx context.Context, graceSeconds int) (files.JanitorStats, error) {
	if a.Files == nil {
		return files.JanitorStats{}, errorf(CodeInternal, "files service not configured")
	}
	if graceSeconds < 0 {
		return files.JanitorStats{}, errorf(CodeInvalidArgument, "grace must be >= 0")
	}
	stats, err := a.Files.Janitor(ctx, time.Duration(graceSeconds)*time.Second)
	if err != nil {
		return stats, errorf(CodeInternal, "janitor: %v", err)
	}
	return stats, nil
}

// translateFilesErr maps files-package sentinels into stable api codes.
func translateFilesErr(kind, slug string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, files.ErrAreaNotFound):
		return notFound("area", slug)
	case errors.Is(err, files.ErrAreaInUse):
		return errorf(CodeInvalidArgument, "area %q is in use", slug)
	case errors.Is(err, files.ErrAreaTaken):
		return duplicateSlug(slug)
	case errors.Is(err, files.ErrNotFound):
		return notFound(kind, slug)
	case errors.Is(err, files.ErrSlugTaken):
		return duplicateSlug(slug)
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return err
	}
	return errorf(CodeInternal, "%v", err)
}
