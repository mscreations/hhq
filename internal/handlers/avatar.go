// Copyright (C) 2026 Jon Shaulis
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
)

// maxAvatarBytes caps both the interactive dashboard upload (enforced via
// http.MaxBytesReader) and a children.json avatar_file entry (enforced with a
// plain len() check after os.ReadFile) - a photo doesn't need to be any
// larger than this to look fine as a kiosk/dashboard thumbnail.
const maxAvatarBytes = 2 << 20 // 2 MiB

// maxAvatarDimension guards against decompression-bomb-style images that
// report enormous pixel dimensions despite a small file size.
const maxAvatarDimension = 4096

// validateAvatarBytes is the single validation path shared by the
// interactive upload handler (UploadChildAvatar) and BootstrapChildren's
// avatar_file reconciliation, so the two can never accept different sets of
// images. Callers are responsible for enforcing maxAvatarBytes on the raw
// size before calling this. Returns the sniffed content type on success.
func validateAvatarBytes(data []byte) (contentType string, err error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty file")
	}

	sniffed := http.DetectContentType(data)
	switch sniffed {
	case "image/png", "image/jpeg", "image/gif":
	default:
		return "", fmt.Errorf("unsupported file type %q (must be PNG, JPEG, or GIF)", sniffed)
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("not a valid image: %w", err)
	}
	if cfg.Width > maxAvatarDimension || cfg.Height > maxAvatarDimension {
		return "", fmt.Errorf("image is too large (%dx%d, max %dx%d)", cfg.Width, cfg.Height, maxAvatarDimension, maxAvatarDimension)
	}

	return sniffed, nil
}

// ServeAvatar is the unauthenticated route the kiosk and parent dashboard
// both load avatar <img> tags from - unauthenticated to match the kiosk's
// own no-login model, since avatars are displayed there. Setting an ETag
// before calling http.ServeContent gets If-None-Match/304 handling for
// free from the stdlib, pairing with the ?v= cache-busting query param
// User.AvatarURL adds to the URL.
func (a *App) ServeAvatar(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	data, contentType, updatedAt, ok, err := a.Users.GetAvatarByID(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, updatedAt.UnixNano()))
	http.ServeContent(w, r, "", updatedAt, bytes.NewReader(data))
}

// UploadChildAvatar handles the dashboard's per-child avatar upload form
// (multipart/form-data, field "avatar"). Allowed regardless of
// BootstrapManaged - unlike name/color, avatars have no children.json
// equivalent for a dashboard-created child to conflict with, and for a
// bootstrap-managed child with avatar_file configured, the next
// BootstrapChildren reconcile pass simply overwrites this again (the
// confirmed, expected "config wins on restart" behavior).
func (a *App) UploadChildAvatar(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	user, err := a.Users.GetByID(r.Context(), id)
	if err == models.ErrNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondError := a.respondChildrenError
	successFragment := "parent/_children"
	if user.Role == models.RoleParent {
		respondError = a.respondParentsError
		successFragment = "parent/_parents"
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes)
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		logging.Warnf("parent: avatar upload for user id=%d rejected: %v", id, err)
		respondError(w, r, "That photo is too large or the upload was invalid (max 2 MB).")
		return
	}
	file, _, err := r.FormFile("avatar")
	if err != nil {
		respondError(w, r, "Please choose a photo to upload.")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		logging.Warnf("parent: reading avatar upload for user id=%d: %v", id, err)
		respondError(w, r, "That photo is too large or the upload was invalid (max 2 MB).")
		return
	}
	contentType, err := validateAvatarBytes(data)
	if err != nil {
		logging.Warnf("parent: avatar upload for user id=%d rejected: %v", id, err)
		respondError(w, r, err.Error())
		return
	}

	if err := a.Users.SetAvatar(r.Context(), id, data, contentType); err != nil {
		logging.Errorf("parent: storing avatar for user id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: set avatar for user id=%d", id)
	a.respondAfterMutation(w, r, successFragment)
}

// RemoveChildAvatar clears a user's avatar, falling back to their color
// swatch everywhere it's displayed. Allowed regardless of BootstrapManaged,
// same rationale as UploadChildAvatar.
func (a *App) RemoveChildAvatar(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	user, err := a.Users.GetByID(r.Context(), id)
	if err == models.ErrNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Users.ClearAvatar(r.Context(), id); err != nil {
		logging.Errorf("parent: clearing avatar for user id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: cleared avatar for user id=%d", id)
	successFragment := "parent/_children"
	if user.Role == models.RoleParent {
		successFragment = "parent/_parents"
	}
	a.respondAfterMutation(w, r, successFragment)
}

// applyChildAvatarFile is BootstrapChildren's counterpart to
// UploadChildAvatar for a children.json entry's avatar_file field: reads and
// validates the file the same way an interactive upload is validated, then
// only calls Users.SetAvatar if the file's contents actually changed since
// the last reconcile pass (compared via sha256 checksum) - this avoids
// rewriting the row and bumping avatar_updated_at (which cache-busts the
// served image's URL) on every single startup when nothing changed.
func (a *App) applyChildAvatarFile(ctx context.Context, childID int, avatarFile string) error {
	path := avatarFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(config.ConfigDir(), path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %q: %w", path, err)
	}
	if len(data) > maxAvatarBytes {
		return fmt.Errorf("%q is too large (max 2 MB)", path)
	}
	contentType, err := validateAvatarBytes(data)
	if err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}

	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])
	existingChecksum, ok, err := a.Users.GetAvatarChecksum(ctx, childID)
	if err != nil {
		return err
	}
	if ok && existingChecksum == checksum {
		return nil // unchanged since the last reconcile pass
	}

	return a.Users.SetAvatar(ctx, childID, data, contentType)
}

// respondChildrenError is respondParentsError's counterpart for the
// "Children" card's one failure case (a rejected avatar upload). A plain
// form post redirects with the message in a query param (read back by
// buildParentDashboardData on the next GET); an htmx request instead gets
// the "_children" fragment re-rendered immediately with the error.
func (a *App) respondChildrenError(w http.ResponseWriter, r *http.Request, message string) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/parent?children_error="+url.QueryEscape(message), http.StatusSeeOther)
		return
	}
	data, err := a.buildParentDashboardData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.ChildrenError = message
	a.renderFragment(w, "parent/_children", *data)
}
