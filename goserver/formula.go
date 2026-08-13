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
	tokens, err := tokenizeFormula(input)
	if err != nil {
		return 0, err
	}
	parser := formulaParser{tokens: tokens}
	node, err := parser.parseExpression()
	if err != nil {
		return 0, err
	}
	if parser.peek().kind != "eof" {
		return 0, fmt.Errorf("多余的内容 %q", parser.peek().value)
	}
	return node.evaluate(env)
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
