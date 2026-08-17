package tax

import "testing"

func TestIsEU(t *testing.T) {
	if len(euMembers) != 27 {
		t.Errorf("%d member states, want 27", len(euMembers))
	}
	for _, code := range []string{"BG", "AT", "DE", "HR", "GR", "IE", "SE"} {
		if !IsEU(code) {
			t.Errorf("IsEU(%q) = false, want true", code)
		}
	}
	for _, code := range []string{"GB", "CH", "NO", "IS", "LI", "US", "XX", "", "bg"} {
		if IsEU(code) {
			t.Errorf("IsEU(%q) = true, want false", code)
		}
	}
}
