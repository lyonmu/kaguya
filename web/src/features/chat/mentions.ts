export interface ActiveMention { start: number; end: number; query: string }
export function activeMention(text: string, caret: number): ActiveMention | undefined {
  const match = /(?:^|\s)@([^\s@]*)$/.exec(text.slice(0, caret))
  if (!match) return
  return { start: match.index + (match[0].startsWith('@') ? 0 : 1), end: caret, query: match[1] }
}
