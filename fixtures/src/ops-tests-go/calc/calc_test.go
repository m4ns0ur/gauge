package calc

import "testing"

func TestAverage(t *testing.T) {
	cases := []struct {
		values []int
		want   float64
	}{
		{[]int{2, 3}, 2.5},
		{[]int{1, 2, 3, 4}, 2.5},
		{[]int{-1, 1}, 0},
		{nil, 0},
	}
	for _, tc := range cases {
		if got := Average(tc.values); got != tc.want {
			t.Errorf("Average(%v) = %v, want %v", tc.values, got, tc.want)
		}
	}
}
