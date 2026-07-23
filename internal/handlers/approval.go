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
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/email"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
)

// notifyParentsOfCompletion emails every active parent user with approve/reject
// buttons for the given chore instance. Called as a goroutine from
// KioskCompleteChore so a slow SMTP server never makes the kiosk feel laggy.
func (a *App) notifyParentsOfCompletion(ctx context.Context, instanceID int) {
	// Use a fresh context with its own timeout since the original request's
	// context may be canceled by the time this goroutine runs.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logging.Debugf("approval: notifying parents of completion for chore instance id=%d", instanceID)

	inst, err := a.ChoreInstances.GetByID(ctx, instanceID)
	if err != nil {
		logging.Errorf("notifyParentsOfCompletion: loading instance %d: %v", instanceID, err)
		return
	}

	parents, err := a.Users.ListParents(ctx)
	if err != nil {
		logging.Errorf("notifyParentsOfCompletion: loading parents: %v", err)
		return
	}
	if len(parents) == 0 {
		logging.Warnf("notifyParentsOfCompletion: no parent users configured, skipping email for instance %d", instanceID)
		return
	}

	approveToken := a.Approval.Sign(inst.ID, auth.ActionApprove, a.Cfg.ApprovalLinkTTL)
	rejectToken := a.Approval.Sign(inst.ID, auth.ActionReject, a.Cfg.ApprovalLinkTTL)

	appTitle := a.appTitle(ctx)

	subject, htmlBody, err := email.RenderApprovalEmail(email.ApprovalEmailData{
		AppName:    appTitle,
		ChildName:  inst.ChildName,
		ChoreName:  inst.ChoreName,
		DueDate:    inst.DueDate.Format("Monday, Jan 2"),
		Points:     inst.Points,
		ApproveURL: fmt.Sprintf("%s/approval/respond?token=%s", a.Cfg.PublicBaseURL, approveToken),
		RejectURL:  fmt.Sprintf("%s/approval/respond?token=%s", a.Cfg.PublicBaseURL, rejectToken),
	})
	if err != nil {
		logging.Errorf("notifyParentsOfCompletion: rendering email: %v", err)
		return
	}

	var to []string
	for _, p := range parents {
		if p.Email.Valid {
			to = append(to, p.Email.String)
		}
	}
	logging.Debugf("approval: emailing %d parent(s) about %q's chore %q", len(to), inst.ChildName, inst.ChoreName)

	if err := a.Mailer.Send(appTitle, to, subject, htmlBody); err != nil {
		logging.Errorf("notifyParentsOfCompletion: sending email: %v", err)
	}
}

// ApprovalRespond is the public (but signed-token-protected) endpoint a parent
// hits by clicking Approve/Reject in their email. No login required — the
// unguessable, time-limited, single-purpose token IS the authentication.
//
// NOTE: Because email clients/scanners sometimes pre-fetch links, ideally this
// would render a confirmation page with a POST button rather than acting on a
// bare GET. For v1 simplicity (and because the token is single-use in effect —
// the underlying chore_instance can only transition out of pending_approval
// once) this acts directly on GET. If you find email link-scanners are
// triggering false approvals, switch this to a confirm-then-POST flow.
func (a *App) ApprovalRespond(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	instanceID, action, err := a.Approval.Verify(token)
	if err != nil {
		logging.Warnf("approval: rejected an invalid/expired token from %s: %v", r.RemoteAddr, err)
		http.Error(w, "This approval link is invalid or has expired.", http.StatusBadRequest)
		return
	}

	approve := action == auth.ActionApprove
	logging.Debugf("approval: token verified for instance id=%d action=%s", instanceID, action)

	// decidedBy is left as 0 (-> stored as NULL) here since we don't know which
	// parent clicked without requiring login — acceptable per the "no login
	// needed to click" requirement. If you want per-parent attribution, add a
	// lightweight "which parent are you" picker on this confirmation page instead.
	err = a.ChoreInstances.Decide(r.Context(), instanceID, approve, 0)
	if err == models.ErrInvalidTransition {
		logging.Infof("approval: instance id=%d was already decided before this click", instanceID)
		fmt.Fprint(w, "<html><body style='font-family:sans-serif;padding:40px;text-align:center;background-color:#111418;color:#f0f2f5;'>"+
			"<h2>Already handled</h2><p>This chore has already been approved or rejected.</p></body></html>")
		return
	}
	if err != nil {
		logging.Errorf("approval: deciding instance id=%d: %v", instanceID, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	verb := "approved"
	if !approve {
		verb = "rejected"
	}
	logging.Infof("approval: instance id=%d %s via email link", instanceID, verb)
	fmt.Fprintf(w, "<html><body style='font-family:sans-serif;padding:40px;text-align:center;background-color:#111418;color:#f0f2f5;'>"+
		"<h2>Chore %s</h2><p>Thanks - you can close this window.</p></body></html>", verb)
}
