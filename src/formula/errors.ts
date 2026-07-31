export class FormulaError extends Error {
  constructor(message: string, public readonly pos: number) {
    super(message);
    this.name = 'FormulaError';
  }
}

export function err(msg: string, pos: number): FormulaError {
  return new FormulaError(`${msg}（位置 ${pos + 1}）`, pos);
}
