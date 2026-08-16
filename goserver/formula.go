package main

import (
	"math/rand"

	"bilibili-live-gift-panel/internal/gameplay"
)

// formulaRandomIntn is retained as the desktop deterministic replay/test
// hook. Formula parsing and evaluation live only in internal/gameplay.
var formulaRandomIntn = rand.Intn

func evaluateFormula(input string, environment map[string]float64) (float64, error) {
	return gameplay.EvaluateFormula(input, environment, formulaRandomIntn)
}

func validateFormula(input string, environment map[string]float64) error {
	return gameplay.ValidateFormula(input, environment)
}

func rewriteFormulaIdentifier(input, oldName, newName string) (string, error) {
	return gameplay.RewriteFormulaIdentifier(input, oldName, newName)
}

// formulaToken/formulaParser preserve the old root-package validation seam
// used by config_store.go. Syntax parsing itself remains single-sourced in
// internal/gameplay.
type formulaToken struct {
	kind  string
	value string
}

func tokenizeFormula(input string) ([]formulaToken, error) {
	if err := gameplay.ValidateFormulaSyntax(input); err != nil {
		return nil, err
	}
	return []formulaToken{{kind: "eof"}}, nil
}

type formulaParser struct {
	tokens []formulaToken
}

func (parser *formulaParser) parseExpression() (any, error) { return struct{}{}, nil }
func (parser *formulaParser) peek() formulaToken            { return parser.tokens[0] }
