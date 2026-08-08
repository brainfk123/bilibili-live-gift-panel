import { describe, expect, it } from 'vitest';
import { normalizeAssistantMarkdown, parseAssistantMarkdown } from '../src/ui/config/assistant-markdown';

describe('assistant Markdown', () => {
  it('removes blank padding and parses a bold conclusion with an ordered list', () => {
    expect(parseAssistantMarkdown('\n\n**已连接直播间。**\n\n1. 填写房间号\n2. 点击 `连接`\n\n')).toEqual([
      { kind: 'paragraph', content: [{ kind: 'strong', text: '已连接直播间。' }] },
      {
        kind: 'ordered-list',
        items: [
          [{ kind: 'text', text: '填写房间号' }],
          [{ kind: 'text', text: '点击 ' }, { kind: 'code', text: '连接' }],
        ],
      },
    ]);
  });

  it('keeps HTML and Markdown links as inert text', () => {
    expect(parseAssistantMarkdown('**安全回答**\n<script>alert(1)</script> [打开](https://example.test)')).toEqual([
      {
        kind: 'paragraph',
        content: [
          { kind: 'strong', text: '安全回答' },
          { kind: 'text', text: ' <script>alert(1)</script> [打开](https://example.test)' },
        ],
      },
    ]);
  });

  it('removes a conclusion label and guarantees a bold first sentence', () => {
    expect(normalizeAssistantMarkdown('\n结论：已连接直播间。\n\n1. 继续操作')).toBe('**已连接直播间。**\n\n1. 继续操作');
    expect(normalizeAssistantMarkdown('设置名称和起始值。填写名称。选择显示格式。')).toBe(
      '**设置名称和起始值。**\n\n填写名称。选择显示格式。',
    );
    expect(normalizeAssistantMarkdown('**结论：可以使用。**\n后续说明')).toBe('**可以使用。**\n后续说明');
  });

  it('removes formatting labels repeated by a small model', () => {
    expect(normalizeAssistantMarkdown(
      '**加粗结论： 按照步骤：复制专属链接并添加为 OBS 浏览器来源。**\n\n- 在属性卡片中复制 OBS 链接。',
    )).toBe('**按照步骤：复制专属链接并添加为 OBS 浏览器来源。**\n\n- 在属性卡片中复制 OBS 链接。');
    expect(normalizeAssistantMarkdown('粗体结论：可以直接添加。\n\n1. 复制链接')).toBe(
      '**可以直接添加。**\n\n1. 复制链接',
    );
  });

  it('removes redundant trailing evidence notes but keeps meaningful parentheses', () => {
    expect(normalizeAssistantMarkdown(
      '1. 进入属性玩法，点击添加属性。\n2. 选择玩法模板。\n\n（已根据帮助条目完成属性玩法设置）',
    )).toBe('1. 进入属性玩法，点击添加属性。\n2. 选择玩法模板。');
    expect(normalizeAssistantMarkdown('操作完成后重新启动（仅首次安装需要）。')).toBe(
      '**操作完成后重新启动（仅首次安装需要）。**',
    );
  });
});
