export interface ActiveMention { start: number; end: number; query: string }
export function activeMention(text: string, caret: number): ActiveMention | undefined {
  const match = /(?:^|\s)@(?:"((?:\\.|[^"\\])*)|([^\s"@]*))$/.exec(text.slice(0, caret))
  if (!match) return
  return { start: match.index + (match[0].startsWith('@') ? 0 : 1), end: caret, query: match[1] ?? match[2] ?? '' }
}
export function mentionToken(path: string) { return `@${JSON.stringify(path)}` }
export function referencedFiles(text: string): string[] {
  const paths: string[] = []
  for (const match of text.matchAll(/(?:^|\s)@("(?:\\.|[^"\\])*"|[^\s"@]+)/g)) {
    try {
      const path = match[1].startsWith('"') ? JSON.parse(match[1]) as string : match[1]
      if (!paths.includes(path)) paths.push(path)
    } catch { /* An unfinished quoted mention is still being edited. */ }
  }
  return paths
}
