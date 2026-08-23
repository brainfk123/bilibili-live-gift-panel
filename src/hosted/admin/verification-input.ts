import { mountVerificationCode, type VerificationCodeControl } from '../verification-code';

export type VerificationInputControl = VerificationCodeControl;

export function mountVerificationInput(
  root: HTMLElement,
  options: { label: string; initialValue?: string; onComplete(code: string): void },
): VerificationInputControl {
  return mountVerificationCode(root, options);
}

