package domain

import "testing"

func TestIsValidTenor(t *testing.T) {
	tests := []struct {
		tenor Tenor
		want  bool
	}{
		{TenorSpot, true},
		{TenorForward, true},
		{Tenor("swap"), false},
		{Tenor(""), false},
	}
	for _, tt := range tests {
		if got := IsValidTenor(tt.tenor); got != tt.want {
			t.Errorf("IsValidTenor(%q) = %v, want %v", tt.tenor, got, tt.want)
		}
	}
}

func TestIsValidHedgeType(t *testing.T) {
	tests := []struct {
		ht   HedgeType
		want bool
	}{
		{TypeSpot, true},
		{TypeForward, true},
		{HedgeType("option"), false},
		{HedgeType(""), false},
	}
	for _, tt := range tests {
		if got := IsValidHedgeType(tt.ht); got != tt.want {
			t.Errorf("IsValidHedgeType(%q) = %v, want %v", tt.ht, got, tt.want)
		}
	}
}