export interface VerificationCodeControl {
  focus(): void;
  clear(): void;
  setBusy(busy: boolean): void;
  dispose(): void;
}

export function mountVerificationCode(
  root: HTMLElement,
  options: { label: string; onComplete(code: string): void },
): VerificationCodeControl {
  const document = root.ownerDocument;
  const input = document.createElement('input');
  input.type = 'text';
  input.inputMode = 'numeric';
  input.autocomplete = 'one-time-code';
  input.className = 'hosted-code-input';
  input.setAttribute('pattern', '[0-9]*');
  input.setAttribute('aria-label', options.label);
  input.setAttribute('maxlength', '6');

  const cells = Array.from({ length: 6 }, () => {
    const cell = document.createElement('span');
    cell.className = 'hosted-code-cell';
    cell.setAttribute('aria-hidden', 'true');
    return cell;
  });
  let digits = '';
  let completed = '';
  let busy = false;
  let focused = false;
  let disposed = false;
  const render = (): void => {
    input.value = digits;
    cells.forEach((cell, index) => {
      cell.textContent = digits[index] ?? '';
      cell.className = `hosted-code-cell${focused && !busy && digits.length < 6 && index === digits.length ? ' is-active' : ''}`;
    });
  };
  const onInput = (): void => {
    if (disposed || busy) { render(); return; }
    digits = input.value.replace(/[^0-9]/g, '').slice(0, 6);
    render();
    if (digits.length === 6 && digits !== completed) {
      completed = digits;
      options.onComplete(digits);
    } else if (digits.length < 6) {
      completed = '';
    }
  };
  const onFocus = (): void => { if (!disposed) { focused = true; render(); } };
  const onBlur = (): void => { focused = false; render(); };
  input.addEventListener('input', onInput);
  input.addEventListener('focus', onFocus);
  input.addEventListener('blur', onBlur);
  root.replaceChildren(input, ...cells);

  return Object.freeze({
    focus(): void { if (!disposed) input.focus(); },
    clear(): void {
      digits = ''; completed = ''; render();
      if (!disposed) input.focus();
    },
    setBusy(nextBusy: boolean): void {
      busy = nextBusy; input.disabled = nextBusy;
      root.classList?.toggle('is-busy', nextBusy);
    },
    dispose(): void {
      disposed = true; busy = true; focused = false; digits = ''; completed = ''; render();
      input.removeEventListener('input', onInput);
      input.removeEventListener('focus', onFocus);
      input.removeEventListener('blur', onBlur);
      root.replaceChildren();
    },
  });
}
