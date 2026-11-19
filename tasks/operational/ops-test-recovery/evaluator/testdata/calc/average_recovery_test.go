package calc

import "testing"

func TestAverageRecovered(t *testing.T) {
	cases := []struct {
		values []int
		want   float64
	}{
		{[]int{2, 3}, 2.5},
		{[]int{1, 2}, 1.5},
		{[]int{-2, -3}, -2.5},
		{[]int{7}, 7},
		{nil, 0},
	}
	for _, tc := range cases {
		if got := Average(tc.values); got != tc.want {
			t.Errorf("Average(%v) = %v, want %v", tc.values, got, tc.want)
		}
	}
}
