import { describe, expect, it, vi } from 'vitest';
import {
  createServerContinuityController,
  plannedUpdateRestartExpected,
  setPlannedUpdateRestart,
} from '../src/server-continuity';

describe('server continuity during automatic updates', () => {
  it('tracks and resets whether the next disconnect is a planned update restart', () => {
    setPlannedUpdateRestart(true);
    expect(plannedUpdateRestartExpected()).toBe(true);
    setPlannedUpdateRestart(false);
    expect(plannedUpdateRestartExpected()).toBe(false);
  });
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

  it('keeps the planned update message visible while the server restarts', () => {
    const actions = { show: vi.fn(), hide: vi.fn(), reload: vi.fn() };
    const controller = createServerContinuityController(actions);

    controller.ready('0.4.5');
    controller.unavailable(true);

    expect(actions.show).toHaveBeenLastCalledWith('正在替换程序并重新连接…');
  });

  it('reloads after recovery when the first ready event was missed', () => {
    const actions = { show: vi.fn(), hide: vi.fn(), reload: vi.fn() };
    const controller = createServerContinuityController(actions);

    controller.unavailable();
    controller.ready('0.4.5');

    expect(actions.reload).toHaveBeenCalledOnce();
  });
});
