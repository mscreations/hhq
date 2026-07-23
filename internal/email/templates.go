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

package email

import (
	"bytes"
	"fmt"
	"html/template"
)

var approvalTmpl = template.Must(template.New("approval").Parse(`
<!DOCTYPE html>
<html>
<body style="font-family: -apple-system, Helvetica, Arial, sans-serif; background:#f5f5f5; padding:24px;">
  <div style="max-width:480px; margin:0 auto; background:#fff; border-radius:12px; padding:32px; border:1px solid #e5e5e5;">
    <h2 style="margin-top:0; color:#222;">Chore Completed: {{.ChoreName}}</h2>
    <p style="font-size:16px; color:#444;">
      <strong>{{.ChildName}}</strong> marked <strong>"{{.ChoreName}}"</strong> as complete on {{.DueDate}}
      (worth {{.Points}} point{{if ne .Points 1}}s{{end}}).
    </p>
    <p style="color:#666;">Please review and respond:</p>
    <table cellpadding="0" cellspacing="0" style="margin:24px 0;">
      <tr>
        <td style="padding-right:12px;">
          <a href="{{.ApproveURL}}" style="background:#22c55e; color:#fff; text-decoration:none; padding:14px 28px; border-radius:8px; font-weight:bold; display:inline-block;">✓ Approve</a>
        </td>
        <td>
          <a href="{{.RejectURL}}" style="background:#ef4444; color:#fff; text-decoration:none; padding:14px 28px; border-radius:8px; font-weight:bold; display:inline-block;">✗ Reject</a>
        </td>
      </tr>
    </table>
    <p style="font-size:13px; color:#999;">This link expires in 7 days. If you reject, {{.ChildName}} will see it go back to incomplete so they can try again — you may want to follow up with them directly about why.</p>
    <p style="font-size:12px; color:#bbb;">{{.AppName}}</p>
  </div>
</body>
</html>
`))

type ApprovalEmailData struct {
	AppName    string
	ChildName  string
	ChoreName  string
	DueDate    string
	Points     int
	ApproveURL string
	RejectURL  string
}

func RenderApprovalEmail(data ApprovalEmailData) (subject, htmlBody string, err error) {
	var buf bytes.Buffer
	if err := approvalTmpl.Execute(&buf, data); err != nil {
		return "", "", err
	}
	subject = fmt.Sprintf("%s completed: %s", data.ChildName, data.ChoreName)
	return subject, buf.String(), nil
}

var inviteTmpl = template.Must(template.New("invite").Parse(`
<!DOCTYPE html>
<html>
<body style="font-family: -apple-system, Helvetica, Arial, sans-serif; background:#f5f5f5; padding:24px;">
  <div style="max-width:480px; margin:0 auto; background:#fff; border-radius:12px; padding:32px; border:1px solid #e5e5e5;">
    <h2 style="margin-top:0; color:#222;">You've been invited to {{.AppName}}</h2>
    <p style="font-size:16px; color:#444;">
      {{.InviterName}} has invited you, <strong>{{.RecipientName}}</strong>, to join as a parent user.
    </p>
    <p style="color:#666;">Click below to set your password and finish creating your account:</p>
    <table cellpadding="0" cellspacing="0" style="margin:24px 0;">
      <tr>
        <td>
          <a href="{{.AcceptURL}}" style="background:#3B82F6; color:#fff; text-decoration:none; padding:14px 28px; border-radius:8px; font-weight:bold; display:inline-block;">Accept Invite</a>
        </td>
      </tr>
    </table>
    <p style="font-size:13px; color:#999;">This link expires in 7 days.</p>
  </div>
</body>
</html>
`))

type InviteEmailData struct {
	AppName       string
	RecipientName string
	InviterName   string
	AcceptURL     string
}

func RenderInviteEmail(data InviteEmailData) (subject, htmlBody string, err error) {
	var buf bytes.Buffer
	if err := inviteTmpl.Execute(&buf, data); err != nil {
		return "", "", err
	}
	subject = fmt.Sprintf("You've been invited to %s", data.AppName)
	return subject, buf.String(), nil
}

var passwordResetTmpl = template.Must(template.New("password_reset").Parse(`
<!DOCTYPE html>
<html>
<body style="font-family: -apple-system, Helvetica, Arial, sans-serif; background:#f5f5f5; padding:24px;">
  <div style="max-width:480px; margin:0 auto; background:#fff; border-radius:12px; padding:32px; border:1px solid #e5e5e5;">
    <h2 style="margin-top:0; color:#222;">Reset your {{.AppName}} password</h2>
    <p style="font-size:16px; color:#444;">
      We received a request to reset the password for <strong>{{.RecipientName}}</strong>. If you didn't make this request, you can safely ignore this email.
    </p>
    <p style="color:#666;">Click below to choose a new password:</p>
    <table cellpadding="0" cellspacing="0" style="margin:24px 0;">
      <tr>
        <td>
          <a href="{{.ResetURL}}" style="background:#3B82F6; color:#fff; text-decoration:none; padding:14px 28px; border-radius:8px; font-weight:bold; display:inline-block;">Reset Password</a>
        </td>
      </tr>
    </table>
    <p style="font-size:13px; color:#999;">This link expires in 1 hour.</p>
  </div>
</body>
</html>
`))

type PasswordResetEmailData struct {
	AppName       string
	RecipientName string
	ResetURL      string
}

func RenderPasswordResetEmail(data PasswordResetEmailData) (subject, htmlBody string, err error) {
	var buf bytes.Buffer
	if err := passwordResetTmpl.Execute(&buf, data); err != nil {
		return "", "", err
	}
	subject = fmt.Sprintf("Reset your %s password", data.AppName)
	return subject, buf.String(), nil
}
