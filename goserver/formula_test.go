package main

import (
	"math"
	"testing"
)

func TestFormulaParity(t *testing.T) {
	env := map[string]float64{"price": 1000, "加班时间": 100}
	tests := map[string]float64{
		"1+2*3":              7,
		"(1+2)*3":            9,
		"加班时间+price/1000*60": 160,
		"IF(price>=1000,加班时间+60,加班时间+10)": 160,
		"MIN(加班时间+60,120)":                120,
		"ROUND(1.567,2)":                  1.57,
		"ABS(-7)":                         7,
	}
	for formula, expected := range tests {
		actual, err := evaluateFormula(formula, env)
		if err != nil {
			t.Fatalf("%s: %v", formula, err)
		}
		if math.Abs(actual-expected) > 0.000001 {
			t.Fatalf("%s = %v, want %v", formula, actual, expected)
		}
	}
}

func TestFormulaRejectsRemovedCount(t *testing.T) {
	if _, err := evaluateFormula("count+1", map[string]float64{}); err == nil {
		t.Fatal("count unexpectedly remained available")
	}
}

func TestFormulaRandomRange(t *testing.T) {
	for index := 0; index < 100; index++ {
		value, err := evaluateFormula("RANDBETWEEN(10,60)", map[string]float64{})
		if err != nil || value < 10 || value > 60 || value != math.Trunc(value) {
			t.Fatalf("random value = %v, err = %v", value, err)
		}
	}
}
