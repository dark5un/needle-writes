package main

import "testing"

func TestExtractHold(t *testing.T) {
	cases := []struct {
		wantDur string
		in      []string
		want    []string
	}{
		{wantDur: "5s", in: []string{"-hold", "5s", "proofread"}, want: []string{"proofread"}},
		{wantDur: "5s", in: []string{"proofread", "-hold", "5s"}, want: []string{"proofread"}},
		{wantDur: "0s", in: []string{"proofread"}, want: []string{"proofread"}},
		{wantDur: "0s", in: []string{"-hold"}, want: []string{"-hold"}},
		{wantDur: "0s", in: []string{"-hold", "notaduration", "proofread"}, want: []string{"-hold", "notaduration", "proofread"}},
	}
	for _, tc := range cases {
		got, dur := extractHold(tc.in)
		if dur.String() != tc.wantDur {
			t.Errorf("extractHold(%v) dur = %v, want %v", tc.in, dur, tc.wantDur)
		}
		if len(got) != len(tc.want) {
			t.Errorf("extractHold(%v) args = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("extractHold(%v) args = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}
