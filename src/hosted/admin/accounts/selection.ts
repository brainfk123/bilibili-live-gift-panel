export function createAccountSelection() {
  const selected = new Set<number>();
  return Object.freeze({
    toggle(id: number, checked?: boolean): void { if (checked ?? !selected.has(id)) selected.add(id); else selected.delete(id); },
    togglePage(ids: readonly number[]): void { const all = ids.length > 0 && ids.every((id) => selected.has(id)); for (const id of ids) { if (all) selected.delete(id); else selected.add(id); } },
    clear(): void { selected.clear(); },
    has(id: number): boolean { return selected.has(id); },
    values(): number[] { return [...selected]; },
    size(): number { return selected.size; },
  });
}
