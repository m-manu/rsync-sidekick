package remote

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersion_IsTheSharedRelease(t *testing.T) {
	assert.Equal(t, 2, VersionMajor)
	assert.Equal(t, 10, VersionMinor)
	assert.Equal(t, 0, VersionPatch)
	assert.Equal(t, "v2.10.0", Version())
}

func TestCheckAgentVersion(t *testing.T) {
	tests := []struct {
		version string
		want    error
	}{
		{"v2.10.0", nil},
		{"2.10.0", nil},
		{"v2.10", nil},
		{"2.10", nil},
		{"  v2.10.0  ", nil},
		{"2.10.1", nil},
		{"v2.10.1", nil},
		{"v2.9.0", errAgentTooOld},
		{"2.9", errAgentTooOld},
		{"v2.1.0", errAgentTooOld},
		{"v1.10.6", errAgentTooOld},
		{"v2.11.0", errAgentTooNew},
		{"2.11", errAgentTooNew},
		{"v3.0.0", errAgentTooNew},
		{"3.0", errAgentTooNew},
		{"garbage", errAgentTooOld},
		{"", errAgentTooOld},
		{"v2", errAgentTooOld},
		{"v2.10.0-dirty", errAgentTooOld},
		{"v2.10.0 extra", errAgentTooOld},
	}
	for _, tt := range tests {
		err := checkAgentVersion(tt.version)
		if tt.want == nil {
			assert.NoError(t, err, "version: %s", tt.version)
			continue
		}
		assert.ErrorIs(t, err, tt.want, "version: %s", tt.version)
	}
}

func TestClassifyAgentHandshake(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		versionErr error
		probed     string
		probeErr   error
		class      handshakeClass
		wantErr    error
	}{
		{"same version uses the agent", "v2.10.0", nil, "", nil, handshakeUseAgent, nil},
		{"other patch uses the agent", "2.10.1", nil, "", nil, handshakeUseAgent, nil},
		{"missing patch uses the agent", "v2.10", nil, "", nil, handshakeUseAgent, nil},
		{"older agent stops", "v2.1.0", nil, "", nil, handshakeStop, errAgentTooOld},
		{"newer agent stops", "v2.11.0", nil, "", nil, handshakeStop, errAgentTooNew},
		{"unparsed version stops", "garbage", nil, "", nil, handshakeStop, errAgentTooOld},
		{
			name:       "agent without version_request is checked via --version",
			versionErr: ErrVersionRequestUnsupported,
			probed:     "v2.10.0",
			class:      handshakeUseAgent,
		},
		{
			name:       "old --version output stops",
			versionErr: ErrVersionRequestUnsupported,
			probed:     "v2.1.0",
			class:      handshakeStop,
			wantErr:    errAgentTooOld,
		},
		{
			name:       "newer --version output stops",
			versionErr: ErrVersionRequestUnsupported,
			probed:     "v2.11",
			class:      handshakeStop,
			wantErr:    errAgentTooNew,
		},
		{
			name:       "a failed --version probe falls back to SFTP",
			versionErr: ErrVersionRequestUnsupported, probeErr: errors.New("ssh failed"),
			class: handshakeSFTP,
		},
		{
			name:       "a missing agent falls back to SFTP",
			versionErr: errors.New("connection closed"),
			class:      handshakeSFTP,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			class, err := classifyAgentHandshake(tt.version, tt.versionErr, tt.probed, tt.probeErr)
			assert.Equal(t, tt.class, class)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
