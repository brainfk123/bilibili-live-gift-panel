package main

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"unicode"
)

var formulaRandomIntn = rand.Intn

type formulaToken struct {
	kind  string
	value string
	pos   int
}

type formulaNode interface {
	evaluate(map[string]float64) (float64, error)
}

type numberNode float64

func (n numberNode) evaluate(_ map[string]float64) (float64, error) { return float64(n), nil }

type variableNode string

func (n variableNode) evaluate(env map[string]float64) (float64, error) {
	value, ok := env[string(n)]
	if !ok {
		return 0, fmt.Errorf("变量 %q 未定义", string(n))
	}
	return value, nil
}

type unaryNode struct{ operand formulaNode }

func (n unaryNode) evaluate(env map[string]float64) (float64, error) {
	value, err := n.operand.evaluate(env)
	return -value, err
}

type binaryNode struct {
	op          string
	left, right formulaNode
}

func (n binaryNode) evaluate(env map[string]float64) (float64, error) {
	left, err := n.left.evaluate(env)
	if err != nil {
		return 0, err
	}
	right, err := n.right.evaluate(env)
	if err != nil {
		return 0, err
	}
	switch n.op {
	case "+":
		return left + right, nil
	case "-":
		return left - right, nil
	case "*":
		return left * right, nil
	case "/":
		if right == 0 {
			return 0, fmt.Errorf("除数为零")
		}
		return left / right, nil
	case ">":
		return boolNumber(left > right), nil
	case ">=":
		return boolNumber(left >= right), nil
	case "<":
		return boolNumber(left < right), nil
	case "<=":
		return boolNumber(left <= right), nil
	case "=":
		return boolNumber(left == right), nil
	default:
		return 0, fmt.Errorf("未知运算符 %s", n.op)
	}
}

type callNode struct {
	name string
	args []formulaNode
}

func (n callNode) evaluate(env map[string]float64) (float64, error) {
	name := strings.ToUpper(n.name)
	eval := func(index int) (float64, error) { return n.args[index].evaluate(env) }
	switch name {
	case "IF":
		if len(n.args) != 3 {
			return 0, fmt.Errorf("IF 需要 3 个参数")
		}
		condition, err := eval(0)
		if err != nil {
			return 0, err
		}
		if condition != 0 {
			return eval(1)
		}
		return eval(2)
	case "RANDOMCHOICE":
		if len(n.args) == 0 {
			return 0, fmt.Errorf("RANDOMCHOICE 至少需要 1 个参数")
		}
		if len(n.args) == 1 {
			return eval(0)
		}
		return eval(formulaRandomIntn(len(n.args)))
	case "MAX", "MIN":
		if len(n.args) == 0 {
			return 0, fmt.Errorf("%s 至少需要 1 个参数", name)
		}
		value, err := eval(0)
		if err != nil {
			return 0, err
		}
		for index := 1; index < len(n.args); index++ {
			next, err := eval(index)
			if err != nil {
				return 0, err
			}
			if name == "MAX" {
				value = math.Max(value, next)
			} else {
				value = math.Min(value, next)
			}
		}
		return value, nil
	case "ROUND":
		if len(n.args) < 1 || len(n.args) > 2 {
			return 0, fmt.Errorf("ROUND 需要 1-2 个参数")
		}
		value, err := eval(0)
		if err != nil {
			return 0, err
		}
		digits := 0.0
		if len(n.args) == 2 {
			digits, err = eval(1)
			if err != nil {
				return 0, err
			}
		}
		power := math.Pow(10, digits)
		return math.Round(value*power) / power, nil
	case "ABS":
		if len(n.args) != 1 {
			return 0, fmt.Errorf("ABS 需要 1 个参数")
		}
		value, err := eval(0)
		return math.Abs(value), err
	case "FLOOR":
		if len(n.args) != 1 {
			return 0, fmt.Errorf("FLOOR 需要 1 个参数")
		}
		value, err := eval(0)
		return math.Floor(value), err
	case "RAND":
		if len(n.args) != 0 {
			return 0, fmt.Errorf("RAND 不需要参数")
		}
		return rand.Float64(), nil
	case "RANDBETWEEN":
		if len(n.args) != 2 {
			return 0, fmt.Errorf("RANDBETWEEN 需要 2 个参数")
		}
		minimum, err := eval(0)
		if err != nil {
			return 0, err
		}
		maximum, err := eval(1)
		if err != nil {
			return 0, err
		}
		low, high := int(math.Ceil(minimum)), int(math.Floor(maximum))
		if high < low {
			return 0, fmt.Errorf("RANDBETWEEN 最小值不能大于最大值")
		}
		return float64(low + formulaRandomIntn(high-low+1)), nil
	default:
		return 0, fmt.Errorf("未知函数 %q", n.name)
	}
}

func evaluateFormula(input string, env map[string]float64) (float64, error) {
	node, err := parseFormula(input)
	if err != nil {
		return 0, err
	}
	return node.evaluate(env)
}

func validateFormula(input string, env map[string]float64) error {
	node, err := parseFormula(input)
	if err != nil {
		return err
	}
	if err := validateFormulaNode(node, env); err != nil {
		return err
	}
	result, err := validateGuaranteedFormulaSemantics(node)
	if err != nil {
		return err
	}
	if !result.classes.hasFinite() {
		return fmt.Errorf("规则结果不是有效数字")
	}
	return nil
}

func parseFormula(input string) (formulaNode, error) {
	tokens, err := tokenizeFormula(input)
	if err != nil {
		return nil, err
	}
	parser := formulaParser{tokens: tokens}
	node, err := parser.parseExpression()
	if err != nil {
		return nil, err
	}
	if parser.peek().kind != "eof" {
		return nil, fmt.Errorf("多余的内容 %q", parser.peek().value)
	}
	return node, nil
}

func validateFormulaNode(node formulaNode, env map[string]float64) error {
	switch typed := node.(type) {
	case numberNode:
		return nil
	case variableNode:
		if _, ok := env[string(typed)]; !ok {
			return fmt.Errorf("变量 %q 未定义", string(typed))
		}
		return nil
	case unaryNode:
		return validateFormulaNode(typed.operand, env)
	case binaryNode:
		if err := validateFormulaNode(typed.left, env); err != nil {
			return err
		}
		return validateFormulaNode(typed.right, env)
	case callNode:
		name := strings.ToUpper(typed.name)
		argumentCount := len(typed.args)
		switch name {
		case "IF":
			if argumentCount != 3 {
				return fmt.Errorf("IF 需要 3 个参数")
			}
		case "RANDOMCHOICE":
			if argumentCount == 0 {
				return fmt.Errorf("RANDOMCHOICE 至少需要 1 个参数")
			}
		case "MAX", "MIN":
			if argumentCount == 0 {
				return fmt.Errorf("%s 至少需要 1 个参数", name)
			}
		case "ROUND":
			if argumentCount < 1 || argumentCount > 2 {
				return fmt.Errorf("ROUND 需要 1-2 个参数")
			}
		case "ABS", "FLOOR":
			if argumentCount != 1 {
				return fmt.Errorf("%s 需要 1 个参数", name)
			}
		case "RAND":
			if argumentCount != 0 {
				return fmt.Errorf("RAND 不需要参数")
			}
		case "RANDBETWEEN":
			if argumentCount != 2 {
				return fmt.Errorf("RANDBETWEEN 需要 2 个参数")
			}
		default:
			return fmt.Errorf("未知函数 %q", typed.name)
		}
		for _, argument := range typed.args {
			if err := validateFormulaNode(argument, env); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("表达式不合法")
	}
}

type formulaValueClass uint8

const (
	formulaZero formulaValueClass = 1 << iota
	formulaFiniteNonZero
	formulaPositiveInfinity
	formulaNegativeInfinity
	formulaNaN
)

const (
	formulaFinite   = formulaZero | formulaFiniteNonZero
	formulaInfinity = formulaPositiveInfinity | formulaNegativeInfinity
	formulaTop      = formulaFinite | formulaInfinity | formulaNaN
)

func (classes formulaValueClass) hasFinite() bool { return classes&formulaFinite != 0 }

type formulaSemanticResult struct {
	classes formulaValueClass
	exact   bool
	value   float64
}

func exactFormulaSemanticResult(value float64) formulaSemanticResult {
	classes := formulaFiniteNonZero
	if value == 0 {
		classes = formulaZero
	} else if math.IsInf(value, 1) {
		classes = formulaPositiveInfinity
	} else if math.IsInf(value, -1) {
		classes = formulaNegativeInfinity
	} else if math.IsNaN(value) {
		classes = formulaNaN
	}
	return formulaSemanticResult{classes: classes, exact: true, value: value}
}

func formulaClassMembers(classes formulaValueClass) []formulaValueClass {
	members := make([]formulaValueClass, 0, 4)
	for _, class := range []formulaValueClass{formulaZero, formulaFiniteNonZero, formulaPositiveInfinity, formulaNegativeInfinity, formulaNaN} {
		if classes&class != 0 {
			members = append(members, class)
		}
	}
	return members
}

func abstractBinaryFormulaClasses(op string, left, right formulaValueClass) (formulaValueClass, bool) {
	if left == formulaNaN || right == formulaNaN {
		if op == ">" || op == ">=" || op == "<" || op == "<=" || op == "=" {
			return formulaFinite, false
		}
		return formulaNaN, false
	}
	if op == ">" || op == ">=" || op == "<" || op == "<=" || op == "=" {
		return formulaFinite, false
	}
	switch op {
	case "+", "-":
		leftInfinite := left&formulaInfinity != 0
		rightInfinite := right&formulaInfinity != 0
		if leftInfinite && rightInfinite {
			if op == "+" && left == right {
				return left, false
			}
			if op == "-" && left != right {
				return left, false
			}
			return formulaNaN, false
		}
		if leftInfinite {
			return left, false
		}
		if rightInfinite {
			if op == "+" {
				return right, false
			}
			if right == formulaPositiveInfinity {
				return formulaNegativeInfinity, false
			}
			return formulaPositiveInfinity, false
		}
		if left == formulaZero && right == formulaZero {
			return formulaZero, false
		}
		return formulaFinite | formulaInfinity, false
	case "*":
		leftInfinite := left&formulaInfinity != 0
		rightInfinite := right&formulaInfinity != 0
		if leftInfinite || rightInfinite {
			if left == formulaZero || right == formulaZero {
				return formulaNaN, false
			}
			if leftInfinite && rightInfinite {
				if left == right {
					return formulaPositiveInfinity, false
				}
				return formulaNegativeInfinity, false
			}
			return formulaInfinity, false
		}
		if left == formulaZero || right == formulaZero {
			return formulaZero, false
		}
		return formulaFinite | formulaInfinity, false
	case "/":
		if right == formulaZero {
			return 0, true
		}
		if right&formulaInfinity != 0 {
			if left&formulaInfinity != 0 {
				return formulaNaN, false
			}
			return formulaZero, false
		}
		if left&formulaInfinity != 0 {
			return formulaInfinity, false
		}
		if left == formulaZero {
			return formulaZero, false
		}
		return formulaFinite | formulaInfinity, false
	default:
		return formulaTop, false
	}
}

func validateGuaranteedFormulaSemantics(node formulaNode) (formulaSemanticResult, error) {
	unknownFinite := formulaSemanticResult{classes: formulaFinite}
	top := formulaSemanticResult{classes: formulaTop}
	switch typed := node.(type) {
	case numberNode:
		return exactFormulaSemanticResult(float64(typed)), nil
	case variableNode:
		return unknownFinite, nil
	case unaryNode:
		operand, err := validateGuaranteedFormulaSemantics(typed.operand)
		if err != nil {
			return operand, err
		}
		if operand.exact {
			return exactFormulaSemanticResult(-operand.value), nil
		}
		classes := operand.classes &^ formulaInfinity
		if operand.classes&formulaPositiveInfinity != 0 {
			classes |= formulaNegativeInfinity
		}
		if operand.classes&formulaNegativeInfinity != 0 {
			classes |= formulaPositiveInfinity
		}
		return formulaSemanticResult{classes: classes}, nil
	case binaryNode:
		left, err := validateGuaranteedFormulaSemantics(typed.left)
		if err != nil {
			return top, err
		}
		right, err := validateGuaranteedFormulaSemantics(typed.right)
		if err != nil {
			return top, err
		}
		if left.exact && right.exact {
			if typed.op == "/" && right.value == 0 {
				return top, fmt.Errorf("除数为零")
			}
			value, err := binaryNode{op: typed.op, left: numberNode(left.value), right: numberNode(right.value)}.evaluate(nil)
			if err != nil {
				return top, err
			}
			return exactFormulaSemanticResult(value), nil
		}
		classes := formulaValueClass(0)
		hasValidOutcome := false
		for _, leftClass := range formulaClassMembers(left.classes) {
			for _, rightClass := range formulaClassMembers(right.classes) {
				result, runtimeError := abstractBinaryFormulaClasses(typed.op, leftClass, rightClass)
				if !runtimeError {
					hasValidOutcome = true
					classes |= result
				}
			}
		}
		if !hasValidOutcome {
			return top, fmt.Errorf("除数为零")
		}
		return formulaSemanticResult{classes: classes}, nil
	case callNode:
		name := strings.ToUpper(typed.name)
		switch name {
		case "IF":
			condition, err := validateGuaranteedFormulaSemantics(typed.args[0])
			if err != nil || !condition.exact {
				return top, err
			}
			if condition.value != 0 {
				return validateGuaranteedFormulaSemantics(typed.args[1])
			}
			return validateGuaranteedFormulaSemantics(typed.args[2])
		case "RANDOMCHOICE":
			if len(typed.args) == 1 {
				return validateGuaranteedFormulaSemantics(typed.args[0])
			}
			return top, nil
		case "RAND":
			return unknownFinite, nil
		}

		arguments := make([]formulaSemanticResult, len(typed.args))
		allExact := true
		for index, argument := range typed.args {
			result, err := validateGuaranteedFormulaSemantics(argument)
			if err != nil {
				return top, err
			}
			arguments[index] = result
			allExact = allExact && result.exact
		}
		if name == "RANDBETWEEN" {
			if !allExact {
				return unknownFinite, nil
			}
			low, high := int(math.Ceil(arguments[0].value)), int(math.Floor(arguments[1].value))
			if high < low {
				return top, fmt.Errorf("RANDBETWEEN 最小值不能大于最大值")
			}
			if low == high {
				return exactFormulaSemanticResult(float64(low)), nil
			}
			return unknownFinite, nil
		}
		if !allExact {
			switch name {
			case "MAX", "MIN":
				classes := arguments[0].classes
				for _, argument := range arguments[1:] {
					nextClasses := formulaValueClass(0)
					for _, leftClass := range formulaClassMembers(classes) {
						for _, rightClass := range formulaClassMembers(argument.classes) {
							if leftClass == formulaNaN || rightClass == formulaNaN {
								nextClasses |= formulaNaN
								continue
							}
							if name == "MAX" {
								switch {
								case leftClass == formulaPositiveInfinity || rightClass == formulaPositiveInfinity:
									nextClasses |= formulaPositiveInfinity
								case leftClass == formulaNegativeInfinity:
									nextClasses |= rightClass
								case rightClass == formulaNegativeInfinity:
									nextClasses |= leftClass
								default:
									nextClasses |= formulaFinite
								}
							} else {
								switch {
								case leftClass == formulaNegativeInfinity || rightClass == formulaNegativeInfinity:
									nextClasses |= formulaNegativeInfinity
								case leftClass == formulaPositiveInfinity:
									nextClasses |= rightClass
								case rightClass == formulaPositiveInfinity:
									nextClasses |= leftClass
								default:
									nextClasses |= formulaFinite
								}
							}
						}
					}
					classes = nextClasses
				}
				return formulaSemanticResult{classes: classes}, nil
			case "ROUND":
				value := arguments[0]
				if !value.classes.hasFinite() {
					return formulaSemanticResult{classes: value.classes}, nil
				}
				if len(arguments) == 1 {
					return formulaSemanticResult{classes: value.classes}, nil
				}
				digits := arguments[1]
				if !digits.exact {
					return top, nil
				}
				power := math.Pow(10, digits.value)
				if power == 0 || math.IsInf(power, 0) || math.IsNaN(power) {
					return formulaSemanticResult{classes: formulaNaN}, nil
				}
				if value.classes&^formulaInfinity == 0 {
					return formulaSemanticResult{classes: value.classes}, nil
				}
				return formulaSemanticResult{classes: value.classes | formulaInfinity}, nil
			case "ABS", "FLOOR":
				classes := arguments[0].classes
				if name == "ABS" && classes&formulaNegativeInfinity != 0 {
					classes = classes&^formulaNegativeInfinity | formulaPositiveInfinity
				}
				return formulaSemanticResult{classes: classes}, nil
			default:
				return top, nil
			}
		}
		switch name {
		case "MAX", "MIN":
			value := arguments[0].value
			for _, argument := range arguments[1:] {
				if name == "MAX" {
					value = math.Max(value, argument.value)
				} else {
					value = math.Min(value, argument.value)
				}
			}
			return exactFormulaSemanticResult(value), nil
		case "ROUND":
			digits := 0.0
			if len(arguments) == 2 {
				digits = arguments[1].value
			}
			power := math.Pow(10, digits)
			return exactFormulaSemanticResult(math.Round(arguments[0].value*power) / power), nil
		case "ABS":
			return exactFormulaSemanticResult(math.Abs(arguments[0].value)), nil
		case "FLOOR":
			return exactFormulaSemanticResult(math.Floor(arguments[0].value)), nil
		default:
			return top, nil
		}
	default:
		return top, fmt.Errorf("表达式不合法")
	}
}

func tokenizeFormula(input string) ([]formulaToken, error) {
	runes := []rune(input)
	tokens := make([]formulaToken, 0, len(runes)+1)
	for index := 0; index < len(runes); {
		char := runes[index]
		if unicode.IsSpace(char) {
			index++
			continue
		}
		if unicode.IsDigit(char) || char == '.' && index+1 < len(runes) && unicode.IsDigit(runes[index+1]) {
			start := index
			for index < len(runes) && (unicode.IsDigit(runes[index]) || runes[index] == '.') {
				index++
			}
			value := string(runes[start:index])
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return nil, fmt.Errorf("数字 %q 无效", value)
			}
			tokens = append(tokens, formulaToken{kind: "number", value: value, pos: start})
			continue
		}
		if unicode.IsLetter(char) || char == '_' {
			start := index
			for index < len(runes) && (unicode.IsLetter(runes[index]) || unicode.IsDigit(runes[index]) || runes[index] == '_') {
				index++
			}
			tokens = append(tokens, formulaToken{kind: "ident", value: string(runes[start:index]), pos: start})
			continue
		}
		if index+1 < len(runes) {
			two := string(runes[index : index+2])
			if two == ">=" || two == "<=" {
				tokens = append(tokens, formulaToken{kind: "op", value: two, pos: index})
				index += 2
				continue
			}
		}
		if strings.ContainsRune("+-*/><=,", char) {
			tokens = append(tokens, formulaToken{kind: "op", value: string(char), pos: index})
			index++
			continue
		}
		if char == '(' || char == ')' {
			tokens = append(tokens, formulaToken{kind: "paren", value: string(char), pos: index})
			index++
			continue
		}
		return nil, fmt.Errorf("无法识别的字符 %q", char)
	}
	tokens = append(tokens, formulaToken{kind: "eof", pos: len(runes)})
	return tokens, nil
}

type formulaParser struct {
	tokens []formulaToken
	index  int
}

func (p *formulaParser) peek() formulaToken { return p.tokens[p.index] }
func (p *formulaParser) next() formulaToken {
	token := p.tokens[p.index]
	p.index++
	return token
}
func (p *formulaParser) isOperator(value string) bool {
	token := p.peek()
	return token.kind == "op" && token.value == value
}

func (p *formulaParser) parseExpression() (formulaNode, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	token := p.peek()
	if token.kind == "op" && (token.value == ">" || token.value == ">=" || token.value == "<" || token.value == "<=" || token.value == "=") {
		p.next()
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		left = binaryNode{op: token.value, left: left, right: right}
	}
	return left, nil
}

func (p *formulaParser) parseAdditive() (formulaNode, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for p.isOperator("+") || p.isOperator("-") {
		op := p.next().value
		right, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		left = binaryNode{op: op, left: left, right: right}
	}
	return left, nil
}

func (p *formulaParser) parseMultiplicative() (formulaNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.isOperator("*") || p.isOperator("/") {
		op := p.next().value
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = binaryNode{op: op, left: left, right: right}
	}
	return left, nil
}

func (p *formulaParser) parseUnary() (formulaNode, error) {
	if p.isOperator("-") {
		p.next()
		operand, err := p.parseUnary()
		return unaryNode{operand: operand}, err
	}
	return p.parsePrimary()
}

func (p *formulaParser) parsePrimary() (formulaNode, error) {
	token := p.peek()
	switch token.kind {
	case "number":
		p.next()
		value, _ := strconv.ParseFloat(token.value, 64)
		return numberNode(value), nil
	case "ident":
		p.next()
		if p.peek().kind == "paren" && p.peek().value == "(" {
			p.next()
			args := []formulaNode{}
			if !(p.peek().kind == "paren" && p.peek().value == ")") {
				for {
					arg, err := p.parseExpression()
					if err != nil {
						return nil, err
					}
					args = append(args, arg)
					if !p.isOperator(",") {
						break
					}
					p.next()
				}
			}
			if p.peek().kind != "paren" || p.peek().value != ")" {
				return nil, fmt.Errorf("缺少 %q", ")")
			}
			p.next()
			return callNode{name: token.value, args: args}, nil
		}
		return variableNode(token.value), nil
	case "paren":
		if token.value != "(" {
			return nil, fmt.Errorf("表达式不合法")
		}
		p.next()
		node, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != "paren" || p.peek().value != ")" {
			return nil, fmt.Errorf("缺少 %q", ")")
		}
		p.next()
		return node, nil
	default:
		return nil, fmt.Errorf("表达式不合法")
	}
}

func boolNumber(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
