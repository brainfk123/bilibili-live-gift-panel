package main

import (
	"math"
	"strings"
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
		"FLOOR(1.9)":                      1,
		"FLOOR(-1.1)":                     -2,
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

func TestFormulaFloorRequiresExactlyOneArgument(t *testing.T) {
	for _, formula := range []string{"FLOOR()", "FLOOR(1,2)"} {
		if _, err := evaluateFormula(formula, map[string]float64{}); err == nil {
			t.Fatalf("%s unexpectedly accepted", formula)
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

func TestFormulaRandomChoiceSelectsOneLazyArgument(t *testing.T) {
	tests := []struct {
		name    string
		formula string
		index   int
	}{
		{name: "first", formula: "RANDOMCHOICE(舰长+3,1/0,missing)", index: 0},
		{name: "middle", formula: "RANDOMCHOICE(1/0,舰长+3,missing)", index: 1},
		{name: "last", formula: "RANDOMCHOICE(1/0,missing,舰长+3)", index: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := formulaRandomIntn
			t.Cleanup(func() { formulaRandomIntn = original })
			calls := 0
			formulaRandomIntn = func(limit int) int {
				calls++
				if limit != 3 {
					t.Fatalf("limit = %d, want 3", limit)
				}
				return test.index
			}
			got, err := evaluateFormula(test.formula, map[string]float64{"舰长": 2})
			if err != nil || got != 5 || calls != 1 {
				t.Fatalf("got=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestFormulaRandomChoiceSingleArgumentDoesNotDraw(t *testing.T) {
	original := formulaRandomIntn
	t.Cleanup(func() { formulaRandomIntn = original })
	formulaRandomIntn = func(int) int { t.Fatal("single argument drew randomness"); return 0 }
	got, err := evaluateFormula("RANDOMCHOICE(7)", nil)
	if err != nil || got != 7 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestFormulaRandomChoiceRejectsZeroArguments(t *testing.T) {
	_, err := evaluateFormula("RANDOMCHOICE()", nil)
	if err == nil || !strings.Contains(err.Error(), "RANDOMCHOICE 至少需要 1 个参数") {
		t.Fatalf("error = %v", err)
	}
}

func TestFormulaRandomChoiceReturnsSelectedArgumentError(t *testing.T) {
	original := formulaRandomIntn
	t.Cleanup(func() { formulaRandomIntn = original })
	formulaRandomIntn = func(int) int { return 1 }
	_, err := evaluateFormula("RANDOMCHOICE(10,1/0)", nil)
	if err == nil || !strings.Contains(err.Error(), "除数为零") {
		t.Fatalf("error = %v", err)
	}
}

func TestFormulaValidationChecksStructureWithoutEvaluation(t *testing.T) {
	original := formulaRandomIntn
	t.Cleanup(func() { formulaRandomIntn = original })
	formulaRandomIntn = func(int) int {
		t.Fatal("structural validation drew randomness")
		return 0
	}
	environment := map[string]float64{"积分": 0, "price": 1000}
	for _, formula := range []string{
		"RANDOMCHOICE(积分+10,1/0)",
		"IF(price>=1000,积分+1,1/0)",
		"RANDBETWEEN(10,1)",
	} {
		if err := validateFormula(formula, environment); err != nil {
			t.Fatalf("%s: %v", formula, err)
		}
	}
}

func TestFormulaValidationRejectsInvalidNamesFunctionsAndArity(t *testing.T) {
	environment := map[string]float64{"积分": 0}
	tests := map[string]string{
		"RANDOMCHOICE(积分,missing)": "missing",
		"MYSTERY(积分)":              "未知函数",
		"IF(1,2)":                  "IF 需要 3 个参数",
		"RANDOMCHOICE()":           "RANDOMCHOICE 至少需要 1 个参数",
		"RAND(1)":                  "RAND 不需要参数",
		"积分+":                      "表达式不合法",
	}
	for formula, message := range tests {
		err := validateFormula(formula, environment)
		if err == nil || !strings.Contains(err.Error(), message) {
			t.Fatalf("%s: error = %v, want containing %q", formula, err, message)
		}
	}
}

func TestFormulaNestedLazyRandomCompositionUsesOnlySelectedExpressions(t *testing.T) {
	original := formulaRandomIntn
	t.Cleanup(func() { formulaRandomIntn = original })
	wants := []struct {
		limit int
		value int
	}{{limit: 2, value: 0}, {limit: 3, value: 1}}
	calls := 0
	formulaRandomIntn = func(limit int) int {
		if calls >= len(wants) {
			t.Fatalf("unexpected random draw %d with limit %d", calls+1, limit)
		}
		want := wants[calls]
		calls++
		if limit != want.limit {
			t.Fatalf("draw %d limit = %d, want %d", calls, limit, want.limit)
		}
		return want.value
	}

	formula := "IF(用户身份>=舰长,RANDOMCHOICE(积分+RANDBETWEEN(2,4),1/0),missing)"
	got, err := evaluateFormula(formula, map[string]float64{"用户身份": 2, "舰长": 2, "积分": 10})
	if err != nil || got != 13 || calls != len(wants) {
		t.Fatalf("got=%v err=%v calls=%d, want 13, nil, %d", got, err, calls, len(wants))
	}
}

func TestFormulaLegacyEvaluatorCoverage(t *testing.T) {
	env := map[string]float64{"price": 1000, "加班时间": 100}
	successes := map[string]float64{
		"IF(price>1000,10,1)":         1,
		"MAX(1,5,3)":                  5,
		"MAX(IF(price>500,100,0),50)": 100,
	}
	for formula, want := range successes {
		got, err := evaluateFormula(formula, env)
		if err != nil || math.Abs(got-want) > 0.000001 {
			t.Fatalf("%s = %v, %v; want %v", formula, got, err, want)
		}
	}
	for _, formula := range []string{"1/0", "foo+1", "count+1", "(1+2", "1+2 abc", "1 +"} {
		if _, err := evaluateFormula(formula, env); err == nil {
			t.Fatalf("%s unexpectedly accepted", formula)
		}
	}
}

func TestFormulaRandIsHalfOpenUnitInterval(t *testing.T) {
	for range 200 {
		got, err := evaluateFormula("RAND()", map[string]float64{})
		if err != nil || got < 0 || got >= 1 {
			t.Fatalf("RAND() = %v, %v", got, err)
		}
	}
}
