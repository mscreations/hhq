package weather

import "testing"

func TestDescribeCodeKnownCodes(t *testing.T) {
	for code, want := range wmoCodes {
		got := DescribeCode(code)
		if got.Icon == "" || got.Description == "" {
			t.Errorf("code %d: expected non-empty icon/description, got %+v", code, got)
		}
		if got != want {
			t.Errorf("code %d: got %+v, want %+v", code, got, want)
		}
	}
}

func TestDescribeCodeUnknownFallsBackToCloud(t *testing.T) {
	got := DescribeCode(999)
	if got.Icon != "cloud" {
		t.Errorf("unknown code: got icon %q, want fallback \"cloud\"", got.Icon)
	}
}
