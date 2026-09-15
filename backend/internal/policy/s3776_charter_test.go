package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func TestS3776Charter_Validate(t *testing.T) {
	t.Parallel()
	require.NoError(t, Default().Validate())

	cases := []struct {
		name   string
		mutate func(*Policy)
		ok     bool
		want   string
	}{
		{"password below floor", func(p *Policy) { p.PasswordMinLength = MinPasswordFloor - 1 }, false, "password_min_length"},
		{"otp ttl zero", func(p *Policy) { p.OTPTTLMinutes = 0 }, false, "otp_ttl_minutes"},
		{"cooldown too short", func(p *Policy) { p.OTPCooldownSeconds = MinOTPCooldownSecs - 1 }, false, "otp_cooldown_seconds"},
		{"auto-provision without allowlist", func(p *Policy) { p.GoogleAutoProvision = true }, false, "auto-provisioning requires at least one allowed domain"},
		{"auto-provision with allowlist", func(p *Policy) {
			p.GoogleAutoProvision = true
			p.GoogleAllowedDomains = []string{"example.com"}
		}, true, ""},
		{"admin default role", func(p *Policy) { p.GoogleDefaultRole = authctx.RoleAdmin }, false, "google_default_role"},
		{"viewer default role", func(p *Policy) { p.GoogleDefaultRole = authctx.RoleViewer }, true, ""},
		{"malformed domain", func(p *Policy) { p.GoogleAllowedDomains = []string{"example"} }, false, "invalid domain"},
		{"too many domains", func(p *Policy) {
			p.GoogleAllowedDomains = make([]string, MaxAllowedDomains+1)
			for i := range p.GoogleAllowedDomains {
				p.GoogleAllowedDomains[i] = "a.example.com"
			}
		}, false, "at most"},
		{"unknown admin factor", func(p *Policy) { p.AdminSecondFactor = "sms" }, false, "admin_second_factor"},
		{"totp_only is valid", func(p *Policy) { p.AdminSecondFactor = AdminFactorTOTPOnly }, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := Default()
			tc.mutate(&p)
			err := p.Validate()
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
