package buildinfo

import "testing"

func TestDataStampString(t *testing.T) {
	tests := []struct {
		name  string
		stamp DataStamp
		want  string
	}{
		{
			name:  "both halves, the ordinary case",
			stamp: DataStamp{UpdatedAt: "2026-08-17", Commit: "a1b2c3d4e5f6"},
			want:  "17.08.2026 - a1b2c3d",
		},
		{
			name:  "a date on its own",
			stamp: DataStamp{UpdatedAt: "2026-08-17"},
			want:  "17.08.2026",
		},
		{
			name:  "a commit on its own",
			stamp: DataStamp{Commit: "a1b2c3d4e5f6"},
			want:  "a1b2c3d",
		},
		{
			name:  "a sha already short is left alone",
			stamp: DataStamp{Commit: "a1b2c3d"},
			want:  "a1b2c3d",
		},
		{
			name:  "RFC3339, since a deploy may well hand over a timestamp",
			stamp: DataStamp{UpdatedAt: "2026-08-17T09:30:00Z"},
			want:  "17.08.2026",
		},
		{
			// Not dropped: a deploy that set this wrongly should look wrong on
			// the page rather than exactly like one that never set it.
			name:  "a date nobody can parse is shown as given",
			stamp: DataStamp{UpdatedAt: "last tuesday", Commit: "a1b2c3d"},
			want:  "last tuesday - a1b2c3d",
		},
		{
			name:  "whitespace is not a value",
			stamp: DataStamp{UpdatedAt: "  ", Commit: "\t"},
			want:  "",
		},
		{
			name:  "nothing supplied",
			stamp: DataStamp{},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.stamp.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataStampEmpty(t *testing.T) {
	for _, tt := range []struct {
		name  string
		stamp DataStamp
		want  bool
	}{
		{"nothing supplied", DataStamp{}, true},
		{"whitespace only", DataStamp{UpdatedAt: " ", Commit: "  "}, true},
		{"a date alone counts", DataStamp{UpdatedAt: "2026-08-17"}, false},
		{"a commit alone counts", DataStamp{Commit: "a1b2c3d"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.stamp.Empty(); got != tt.want {
				t.Errorf("Empty() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The default has to be sane, because an unstamped `go build` is what CI runs
// and what anyone gets who builds without the Makefile.
func TestVersionDefaultsToDev(t *testing.T) {
	if Version == "" {
		t.Error("Version is empty; an unstamped build should still say something")
	}
}
