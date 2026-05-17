// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package http exposes Wintermute's HTTPS file-transfer endpoints. The
// router serves POST /upload/<token> and GET /download/<token>; tokens
// are one-shot and minted via internal/files.
package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	netHTTP "net/http"
	"strings"

	"github.com/vaelen/wintermute/internal/files"
)

// HandlerOptions tunes NewHandler.
type HandlerOptions struct {
	// MaxUploadBytes is the per-upload cap. 0 ⇒ 100 MB default.
	MaxUploadBytes int64
	// OnUpload is invoked after every successful upload. May be nil.
	OnUpload func(UploadEvent)
	// Logger; defaults to slog.Default().
	Logger *slog.Logger
}

// UploadEvent is the payload passed to OnUpload after a successful upload.
type UploadEvent struct {
	AccountID int64
	FileID    int64
	Slug      string
	Hash      string
	Size      int64
	MIME      string
}

// UploadResponse is the JSON body returned to a successful POST /upload/<token>.
type UploadResponse struct {
	Slug string `json:"slug"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	MIME string `json:"mime"`
}

const defaultMaxUploadBytes = 100 * 1024 * 1024

// NewHandler returns an http.Handler with /upload/<token> and
// /download/<token> wired to fs.
func NewHandler(fs *files.Service, opts HandlerOptions) netHTTP.Handler {
	if opts.MaxUploadBytes == 0 {
		opts.MaxUploadBytes = defaultMaxUploadBytes
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	mux := netHTTP.NewServeMux()
	mux.Handle("/upload/", uploadHandler(fs, opts))
	mux.Handle("/download/", downloadHandler(fs, opts))
	return mux
}

func uploadHandler(fs *files.Service, opts HandlerOptions) netHTTP.Handler {
	return netHTTP.HandlerFunc(func(w netHTTP.ResponseWriter, r *netHTTP.Request) {
		if r.Method != netHTTP.MethodPost {
			netHTTP.Error(w, "method not allowed", netHTTP.StatusMethodNotAllowed)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, "/upload/")
		if token == "" || strings.ContainsRune(token, '/') {
			netHTTP.Error(w, "bad token path", netHTTP.StatusBadRequest)
			return
		}
		tok, err := fs.RedeemToken(r.Context(), token)
		if err != nil {
			writeTokenError(w, err)
			return
		}
		if tok.Kind != files.KindUpload {
			netHTTP.Error(w, "token is not for upload", netHTTP.StatusBadRequest)
			return
		}

		if r.ContentLength > opts.MaxUploadBytes {
			netHTTP.Error(w, fmt.Sprintf("upload exceeds %d bytes", opts.MaxUploadBytes),
				netHTTP.StatusRequestEntityTooLarge)
			return
		}
		body := netHTTP.MaxBytesReader(w, r.Body, opts.MaxUploadBytes)
		defer body.Close()

		hash, size, mime, err := fs.PutBlob(r.Context(), body)
		if err != nil {
			if _, ok := err.(*netHTTP.MaxBytesError); ok {
				netHTTP.Error(w, "upload too large", netHTTP.StatusRequestEntityTooLarge)
				return
			}
			// Some Go versions wrap the MaxBytesError; sniff by string.
			if strings.Contains(err.Error(), "http: request body too large") {
				netHTTP.Error(w, "upload too large", netHTTP.StatusRequestEntityTooLarge)
				return
			}
			opts.Logger.Error("upload PutBlob", "err", err)
			netHTTP.Error(w, "internal error", netHTTP.StatusInternalServerError)
			return
		}

		fid, err := fs.NewFile(r.Context(), tok.Slug, tok.AccountID, hash, size, mime, "")
		if err != nil {
			opts.Logger.Error("upload NewFile", "err", err)
			if errors.Is(err, files.ErrSlugTaken) {
				netHTTP.Error(w, "slug already taken", netHTTP.StatusConflict)
				return
			}
			netHTTP.Error(w, "internal error", netHTTP.StatusInternalServerError)
			return
		}

		if opts.OnUpload != nil {
			opts.OnUpload(UploadEvent{
				AccountID: tok.AccountID, FileID: fid, Slug: tok.Slug,
				Hash: hash, Size: size, MIME: mime,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(UploadResponse{
			Slug: tok.Slug, Hash: hash, Size: size, MIME: mime,
		})
	})
}

func downloadHandler(fs *files.Service, opts HandlerOptions) netHTTP.Handler {
	return netHTTP.HandlerFunc(func(w netHTTP.ResponseWriter, r *netHTTP.Request) {
		if r.Method != netHTTP.MethodGet && r.Method != netHTTP.MethodHead {
			netHTTP.Error(w, "method not allowed", netHTTP.StatusMethodNotAllowed)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, "/download/")
		if token == "" || strings.ContainsRune(token, '/') {
			netHTTP.Error(w, "bad token path", netHTTP.StatusBadRequest)
			return
		}
		tok, err := fs.RedeemToken(r.Context(), token)
		if err != nil {
			writeTokenError(w, err)
			return
		}
		if tok.Kind != files.KindDownload || tok.FileID == nil {
			netHTTP.Error(w, "token is not for download", netHTTP.StatusBadRequest)
			return
		}
		file, err := fs.GetFileByID(r.Context(), *tok.FileID)
		if err != nil {
			netHTTP.Error(w, "file not found", netHTTP.StatusNotFound)
			return
		}
		blob, err := fs.GetBlob(r.Context(), file.Hash)
		if err != nil {
			opts.Logger.Error("download GetBlob", "err", err, "hash", file.Hash)
			netHTTP.Error(w, "blob missing", netHTTP.StatusInternalServerError)
			return
		}
		defer blob.Close()

		w.Header().Set("Content-Type", file.MIME)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", file.Size))
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename=%q`, file.Slug))
		_, _ = io.Copy(w, blob)
	})
}

func writeTokenError(w netHTTP.ResponseWriter, err error) {
	switch {
	case errors.Is(err, files.ErrTokenNotFound):
		netHTTP.Error(w, "token not found", netHTTP.StatusNotFound)
	case errors.Is(err, files.ErrTokenUsed), errors.Is(err, files.ErrTokenExpired):
		netHTTP.Error(w, "token is no longer valid", netHTTP.StatusGone)
	default:
		netHTTP.Error(w, "internal error", netHTTP.StatusInternalServerError)
	}
}

