const CONFIRM_TIMEOUT_MS = 3_000;

export function bindTwoStepDelete(button: HTMLButtonElement, action: () => void): void {
  let armed = false;
  let resetTimer: ReturnType<typeof globalThis.setTimeout> | undefined;

  const reset = (): void => {
    armed = false;
    if (resetTimer !== undefined) globalThis.clearTimeout(resetTimer);
    resetTimer = undefined;
    if (!button.disabled) button.textContent = '删除';
    button.classList.remove('is-confirming');
    button.setAttribute('aria-pressed', 'false');
  };

  button.setAttribute('aria-pressed', 'false');
  button.onclick = () => {
    if (armed) {
      reset();
      action();
      return;
    }
    armed = true;
    button.textContent = '确定';
    button.classList.add('is-confirming');
    button.setAttribute('aria-pressed', 'true');
    resetTimer = globalThis.setTimeout(reset, CONFIRM_TIMEOUT_MS);
  };
  button.onblur = reset;
}
