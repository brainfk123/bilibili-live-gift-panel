import { describe, it, expect } from 'vitest';
import { evalFormula, collectVars, FormulaError } from '../src/formula';

const env = { price: 1000, 加班时间: 100 };

describe('formula basic arithmetic', () => {
  it('adds', () => expect(evalFormula('1+2', env)).toBe(3));
  it('subtracts', () => expect(evalFormula('5-2', env)).toBe(3));
  it('multiplies', () => expect(evalFormula('3*4', env)).toBe(12));
  it('divides', () => expect(evalFormula('price/1000', env)).toBe(1));
  it('respects precedence', () => expect(evalFormula('2+3*4', env)).toBe(14));
  it('respects parentheses', () => expect(evalFormula('(2+3)*4', env)).toBe(20));
  it('handles unary minus', () => expect(evalFormula('-3+5', env)).toBe(2));
  it('uses variables', () => expect(evalFormula('price/1000*60', env)).toBe(60));
  it('uses chinese attribute name', () => expect(evalFormula('加班时间+50', env)).toBe(150));
});

describe('formula functions', () => {
  it('IF true branch', () => expect(evalFormula('IF(price>=1000, 10, 1)', env)).toBe(10));
  it('IF false branch', () => expect(evalFormula('IF(price>1000, 10, 1)', env)).toBe(1));
  it('MAX', () => expect(evalFormula('MAX(1,5,3)', env)).toBe(5));
  it('MIN', () => expect(evalFormula('MIN(1,5,3)', env)).toBe(1));
  it('ROUND', () => expect(evalFormula('ROUND(1.567, 2)', env)).toBe(1.57));
  it('ABS', () => expect(evalFormula('ABS(-7)', env)).toBe(7));
  it('RAND in [0,1)', () => {
    for (let i = 0; i < 100; i++) {
      const v = evalFormula('RAND()', env);
      expect(v).toBeGreaterThanOrEqual(0);
      expect(v).toBeLessThan(1);
    }
  });
  it('RANDBETWEEN in range', () => {
    for (let i = 0; i < 200; i++) {
      const v = evalFormula('RANDBETWEEN(10, 60)', env);
      expect(v).toBeGreaterThanOrEqual(10);
      expect(v).toBeLessThanOrEqual(60);
      expect(Number.isInteger(v)).toBe(true);
    }
  });
  it('nested functions', () => expect(evalFormula('MAX(IF(price>500, 100, 0), 50)', env)).toBe(100));
});

describe('formula errors', () => {
  it('division by zero throws', () => expect(() => evalFormula('1/0', env)).toThrow(/除数为零/));
  it('unknown variable throws', () => expect(() => evalFormula('foo+1', env)).toThrow(/未定义/));
  it('does not provide the removed count variable', () => expect(() => evalFormula('count+1', env)).toThrow(/未定义/));
  it('missing paren throws', () => expect(() => evalFormula('(1+2', env)).toThrow(/缺少/));
  it('trailing garbage throws', () => expect(() => evalFormula('1+2 abc', env)).toThrow(/多余/));
  it('throws FormulaError with position', () => {
    let caught: unknown;
    try { evalFormula('1 +', env); } catch (e) { caught = e; }
    expect(caught).toBeInstanceOf(FormulaError);
    expect((caught as FormulaError).pos).toBe(3);
  });
  it('collectVars finds variables', () => expect(collectVars('price/1000*加班时间').sort()).toEqual(['price', '加班时间'].sort()));
});
