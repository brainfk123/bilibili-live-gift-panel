import { describe, expect, it } from 'vitest';
import { classifyChanges, parseNameStatusZ } from '../scripts/ci-scope.mjs';

describe('CI scope classification', () => {
  it.each([
    [['src/hosted/main.ts'], 'skip'],
    [['goserver/internal/hosted/runtime/manager.go'], 'skip'],
    [['goserver/internal/gameplay/engine.go'], 'shared'],
    [['src/ui/config/config.ts'], 'desktop'],
    [['goserver/auth_protection_windows.go'], 'desktop-high-risk'],
    [['scripts/build-go.mjs'], 'desktop-high-risk'],
    [['updateapi/server.go'], 'desktop-high-risk'],
    [['future/unknown.file'], 'desktop-high-risk'],
  ])('classifies %j as %s', (paths, windowsLevel) => {
    expect(classifyChanges(paths.map((path) => ({ status: 'M', path })))).toMatchObject({ windowsLevel });
  });

  it('uses the highest level and enables MySQL only for persistence inputs', () => {
    expect(classifyChanges([
      { status: 'M', path: 'src/hosted/main.ts' },
      { status: 'M', path: 'goserver/internal/gameplay/model.go' },
      { status: 'M', path: 'goserver/internal/hosted/store/mysqlstore/store.go' },
    ])).toMatchObject({ windowsLevel: 'shared', runWindows: true, runMySQL: true });
  });

  it('parses rename and deletion records without dropping either path', () => {
    expect(parseNameStatusZ('R100\0src/main.ts\0src/hosted/main.ts\0D\0goserver/tray_windows.go\0'))
      .toEqual([
        { status: 'R100', path: 'src/main.ts', destination: 'src/hosted/main.ts' },
        { status: 'D', path: 'goserver/tray_windows.go' },
      ]);
  });
});
