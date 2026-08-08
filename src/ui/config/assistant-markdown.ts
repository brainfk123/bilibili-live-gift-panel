import { el } from '../common';

export type AssistantMarkdownInline =
  | { kind: 'text'; text: string }
  | { kind: 'strong'; text: string }
  | { kind: 'code'; text: string };

export type AssistantMarkdownBlock =
  | { kind: 'paragraph'; content: AssistantMarkdownInline[] }
  | { kind: 'ordered-list' | 'unordered-list'; items: AssistantMarkdownInline[][] };

export function normalizeAssistantMarkdown(input: string): string {
  let value = input.trim();
  if (!value) return '';
  value = value.replace(
    /(?:\r?\n){1,2}\s*[（(]\s*(?:本回答\s*)?(?:已\s*)?(?:根据|依据|参考|基于)\s*(?:上述|相关|所提供的|项目)?\s*(?:帮助条目|帮助内容|参考帮助|资料|内容)[^（）()\r\n]{0,40}[）)]\s*$/,
    '',
  ).trim();
  const conclusionLabel = '(?:(?:加粗|粗体)\\s*)?结论';
  value = value
    .replace(new RegExp(`^\\*\\*\\s*${conclusionLabel}\\s*[:：]\\s*\\*\\*\\s*`), '')
    .replace(new RegExp(`^\\*\\*\\s*${conclusionLabel}\\s*[:：]\\s*`), '**')
    .replace(new RegExp(`^\\s*(?:#{1,3}\\s*)?${conclusionLabel}\\s*[:：]\\s*`), '');
  if (/^\*\*[^*\n]+\*\*/.test(value) || /^\d+[.)]\s+/.test(value) || /^[-*]\s+/.test(value)) return value;

  const sentence = value.match(/^([\s\S]{1,120}?[。！？!?])([\s\S]*)$/);
  if (sentence) {
    const rest = sentence[2].trim();
    return `**${sentence[1].trim()}**${rest ? `\n\n${rest}` : ''}`;
  }
  const newline = value.indexOf('\n');
  if (newline >= 0) {
    const firstLine = value.slice(0, newline).trim();
    const rest = value.slice(newline).trim();
    return `**${firstLine}**${rest ? `\n\n${rest}` : ''}`;
  }
  return `**${value}**`;
}

export function parseAssistantMarkdown(input: string): AssistantMarkdownBlock[] {
  const lines = input.trim().split(/\r?\n/);
  const blocks: AssistantMarkdownBlock[] = [];
  let index = 0;
  while (index < lines.length) {
    const line = lines[index].trim();
    if (!line) {
      index += 1;
      continue;
    }
    const ordered = line.match(/^\d+[.)]\s+(.+)$/);
    const unordered = line.match(/^[-*]\s+(.+)$/);
    if (ordered || unordered) {
      const kind = ordered ? 'ordered-list' : 'unordered-list';
      const items: AssistantMarkdownInline[][] = [];
      while (index < lines.length) {
        const candidate = lines[index].trim();
        const match = kind === 'ordered-list'
          ? candidate.match(/^\d+[.)]\s+(.+)$/)
          : candidate.match(/^[-*]\s+(.+)$/);
        if (!match) break;
        items.push(parseInline(match[1]));
        index += 1;
      }
      blocks.push({ kind, items });
      continue;
    }
    const paragraph: string[] = [];
    while (index < lines.length) {
      const candidate = lines[index].trim();
      if (!candidate || /^\d+[.)]\s+/.test(candidate) || /^[-*]\s+/.test(candidate)) break;
      paragraph.push(candidate.replace(/^#{1,3}\s+/, ''));
      index += 1;
    }
    blocks.push({ kind: 'paragraph', content: parseInline(paragraph.join(' ')) });
  }
  return blocks;
}

export function renderAssistantMarkdown(input: string): HTMLElement {
  const host = el('div', { class: 'assistant-markdown' });
  for (const block of parseAssistantMarkdown(input)) {
    if (block.kind === 'paragraph') {
      const paragraph = el('p');
      appendInline(paragraph, block.content);
      host.append(paragraph);
      continue;
    }
    const list = el(block.kind === 'ordered-list' ? 'ol' : 'ul');
    for (const item of block.items) {
      const row = el('li');
      appendInline(row, item);
      list.append(row);
    }
    host.append(list);
  }
  return host;
}

function parseInline(input: string): AssistantMarkdownInline[] {
  const result: AssistantMarkdownInline[] = [];
  const matcher = /(\*\*[^*\n]+\*\*|`[^`\n]+`)/g;
  let cursor = 0;
  for (const match of input.matchAll(matcher)) {
    const index = match.index ?? 0;
    if (index > cursor) result.push({ kind: 'text', text: input.slice(cursor, index) });
    const token = match[0];
    if (token.startsWith('**')) result.push({ kind: 'strong', text: token.slice(2, -2) });
    else result.push({ kind: 'code', text: token.slice(1, -1) });
    cursor = index + token.length;
  }
  if (cursor < input.length) result.push({ kind: 'text', text: input.slice(cursor) });
  return result.length > 0 ? result : [{ kind: 'text', text: input }];
}

function appendInline(parent: HTMLElement, content: AssistantMarkdownInline[]): void {
  for (const item of content) {
    if (item.kind === 'strong') parent.append(el('strong', { text: item.text }));
    else if (item.kind === 'code') parent.append(el('code', { text: item.text }));
    else parent.append(el('span', { text: item.text }));
  }
}
