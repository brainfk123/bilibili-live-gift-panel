import { describe, expect, it } from 'vitest';
import * as formulaModule from '../src/formula';
import { collectVars, FormulaError } from '../src/formula';

describe('formula public interface', () => {
  it('exposes draft reference collection without a runtime evaluator', () => {
    expect(Object.keys(formulaModule).sort()).toEqual(['FormulaError', 'collectVars']);
  });
});

describe('formula draft references', () => {
  it('collects unique variables from nested expressions', () => {
    expect(collectVars('MAX(IF(price>500, 加班时间, 0), 加班时间)').sort())
      .toEqual(['price', '加班时间'].sort());
  });

  it('keeps parser position errors for draft feedback', () => {
    let caught: unknown;
    try { collectVars('1 +'); } catch (error) { caught = error; }
    expect(caught).toBeInstanceOf(FormulaError);
    expect((caught as FormulaError).pos).toBe(3);
  });
});
