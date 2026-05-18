// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vaelen/wintermute/internal/boards"
)

// BoardSpec is the input to CreateBoard. Slug and Name are required.
// NetworkSlug defaults to "local". ACL fields use the boards.Level* bands.
type BoardSpec struct {
	Slug          string
	Name          string
	Description   string
	NetworkSlug   string
	AreaTag       string
	ReadMinLevel  int
	PostMinLevel  int
	AdminMinLevel int
}

// BoardACLs is the input to SetBoardACLs. Any field left nil is preserved.
type BoardACLs struct {
	ReadMinLevel  *int
	PostMinLevel  *int
	AdminMinLevel *int
}

// CreateBoard creates a new board. Returns the new row id.
//
// On a duplicate slug, returns a stable duplicate_slug Error. On an
// unknown network slug, returns invalid_argument.
func (a *API) CreateBoard(ctx context.Context, spec BoardSpec) (int64, error) {
	if a.Boards == nil {
		return 0, errorf(CodeInternal, "boards service not configured")
	}
	if spec.Slug == "" {
		return 0, errorf(CodeInvalidArgument, "slug is required")
	}
	if spec.Name == "" {
		return 0, errorf(CodeInvalidArgument, "name is required")
	}
	if spec.ReadMinLevel == 0 {
		spec.ReadMinLevel = boards.LevelPlayer
	}
	if spec.PostMinLevel == 0 {
		spec.PostMinLevel = boards.LevelPlayer
	}
	id, err := a.Boards.CreateBoard(ctx, boards.CreateBoardSpec{
		Slug:          spec.Slug,
		Name:          spec.Name,
		Description:   spec.Description,
		NetworkSlug:   spec.NetworkSlug,
		AreaTag:       spec.AreaTag,
		ReadMinLevel:  spec.ReadMinLevel,
		PostMinLevel:  spec.PostMinLevel,
		AdminMinLevel: spec.AdminMinLevel,
	})
	if err != nil {
		return 0, translateBoardsErr(spec.Slug, err)
	}
	return id, nil
}

// DeleteBoard removes the named board. Returns invalid_argument if any
// posts reference it, not_found if no board with that slug exists.
func (a *API) DeleteBoard(ctx context.Context, slug string) error {
	if a.Boards == nil {
		return errorf(CodeInternal, "boards service not configured")
	}
	if slug == "" {
		return errorf(CodeInvalidArgument, "slug is required")
	}
	if err := a.Boards.DeleteBoard(ctx, slug); err != nil {
		return translateBoardsErr(slug, err)
	}
	return nil
}

// GetBoard returns the named board, or not_found if absent.
func (a *API) GetBoard(ctx context.Context, slug string) (boards.Board, error) {
	if a.Boards == nil {
		return boards.Board{}, errorf(CodeInternal, "boards service not configured")
	}
	b, err := a.Boards.GetBoard(ctx, slug)
	if err != nil {
		return boards.Board{}, translateBoardsErr(slug, err)
	}
	return b, nil
}

// ListAllBoards returns every board regardless of read-ACL. Intended for
// admin Lua scripts; player-tier APIs should use boards.Service.ListBoards
// directly so the read-perms filter applies.
func (a *API) ListAllBoards(ctx context.Context) ([]boards.Board, error) {
	if a.Boards == nil {
		return nil, errorf(CodeInternal, "boards service not configured")
	}
	rows, err := a.DB.Read().QueryContext(ctx, `
		SELECT id, network_id, slug, name, description, area_tag,
		       read_perms, post_perms, admin_perms
		FROM boards
		ORDER BY slug
	`)
	if err != nil {
		return nil, errorf(CodeInternal, "list boards: %v", err)
	}
	defer rows.Close()
	var out []boards.Board
	for rows.Next() {
		var b boards.Board
		if err := rows.Scan(&b.ID, &b.NetworkID, &b.Slug, &b.Name, &b.Description,
			&b.AreaTag, &b.ReadMinLevel, &b.PostMinLevel, &b.AdminMinLevel); err != nil {
			return nil, errorf(CodeInternal, "scan board: %v", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, errorf(CodeInternal, "list boards: %v", err)
	}
	return out, nil
}

// SetBoardACLs updates a board's per-band access levels in place. Fields
// left nil in acls preserve the existing value. Returns not_found if no
// board with that slug exists.
func (a *API) SetBoardACLs(ctx context.Context, slug string, acls BoardACLs) error {
	if a.Boards == nil {
		return errorf(CodeInternal, "boards service not configured")
	}
	if acls.ReadMinLevel == nil && acls.PostMinLevel == nil && acls.AdminMinLevel == nil {
		return errorf(CodeInvalidArgument, "no acl fields supplied")
	}
	for _, lvl := range []*int{acls.ReadMinLevel, acls.PostMinLevel, acls.AdminMinLevel} {
		if lvl == nil {
			continue
		}
		if *lvl < boards.LevelOpen || *lvl > boards.LevelAdmin {
			return errorf(CodeInvalidArgument, "level %d out of range", *lvl)
		}
	}

	var (
		sets []string
		args []any
	)
	if acls.ReadMinLevel != nil {
		sets = append(sets, "read_perms = ?")
		args = append(args, *acls.ReadMinLevel)
	}
	if acls.PostMinLevel != nil {
		sets = append(sets, "post_perms = ?")
		args = append(args, *acls.PostMinLevel)
	}
	if acls.AdminMinLevel != nil {
		sets = append(sets, "admin_perms = ?")
		args = append(args, *acls.AdminMinLevel)
	}
	args = append(args, slug)

	query := fmt.Sprintf(`UPDATE boards SET %s WHERE slug = ?`, strings.Join(sets, ", "))
	var affected int64
	if err := a.DB.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		affected, _ = res.RowsAffected()
		return nil
	}); err != nil {
		return errorf(CodeInternal, "set acls: %v", err)
	}
	if affected == 0 {
		return notFound("board", slug)
	}
	return nil
}

// translateBoardsErr maps boards-package sentinels into stable api codes.
func translateBoardsErr(slug string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, boards.ErrNotFound):
		return notFound("board", slug)
	case errors.Is(err, boards.ErrUnknownNetwork):
		return errorf(CodeInvalidArgument, "unknown network: %v", err)
	case errors.Is(err, boards.ErrForbidden):
		return errorf(CodePermissionDenied, "%v", err)
	case errors.Is(err, boards.ErrHasPosts):
		return errorf(CodeInvalidArgument, "%v", err)
	}
	if isUniqueErr(err) {
		return duplicateSlug(slug)
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return err
	}
	return errorf(CodeInternal, "%v", err)
}

// isUniqueErr reports whether err looks like a SQLite UNIQUE-constraint
// violation. Matched on text because modernc.org/sqlite returns its own
// concrete error type — see the "Error matching" convention in
// CLAUDE.md for why substring matching is justified here.
func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
