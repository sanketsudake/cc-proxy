package capture

import "testing"

func TestParseUserID(t *testing.T) {
	uid := parseUserID(`{"device_id":"dev-1","account_uuid":"acct-9","session_id":"s-1"}`)
	if uid.AccountUUID != "acct-9" || uid.DeviceID != "dev-1" {
		t.Errorf("parsed = %+v", uid)
	}

	// Absent or malformed values yield empty attribution, never an error.
	for _, raw := range []string{"", "not json", "[1,2,3]", "42"} {
		if uid := parseUserID(raw); uid != (userID{}) {
			t.Errorf("raw %q: expected zero value, got %+v", raw, uid)
		}
	}
}
