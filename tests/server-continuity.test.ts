import { describe, expect, it, vi } from 'vitest';
import { createServerContinuityController } from '../src/server-continuity';

describe('server continuity during automatic updates', () => {
  it('shows a temporary outage and hides it when the same backend recovers', () => {
    const actions = { show: vi.fn(), hide: vi.fn(), reload: vi.fn() };
    const controller = createServerContinuityController(actions);

    controller.ready('0.4.4');
    controller.unavailable();
    expect(actions.show).toHaveBeenLastCalledWith('本地服务暂时不可用，可能正在安装更新。页面会自动重连。');
    controller.ready('0.4.4');

    expect(actions.hide).toHaveBeenCalledTimes(2);
    expect(actions.reload).not.toHaveBeenCalled();
  });

  it('reloads the page after a newer backend reconnects', () => {
    const actions = { show: vi.fn(), hide: vi.fn(), reload: vi.fn() };
    const controller = createServerContinuityController(actions);

    controller.ready('0.4.4');
    controller.unavailable();
    controller.ready('0.4.5');

    expect(actions.show).toHaveBeenLastCalledWith('更新完成，正在刷新页面…');
    expect(actions.reload).toHaveBeenCalledOnce();
  });

  it('reloads after recovery when the first ready event was missed', () => {
    const actions = { show: vi.fn(), hide: vi.fn(), reload: vi.fn() };
    const controller = createServerContinuityController(actions);

    controller.unavailable();
    controller.ready('0.4.5');

    expect(actions.reload).toHaveBeenCalledOnce();
  });
});
