import { isRecord, isString } from '../../../api/http'
import { CodeBlock, ExternalLink } from './Markdown'

function parseOutput(text: string): unknown {
  try { return JSON.parse(text) }
  catch { return undefined }
}

function webURL(value: unknown): value is string {
  if (!isString(value)) return false
  try {
    const url = new URL(value)
    return (url.protocol === 'https:' || url.protocol === 'http:') && !!url.hostname && !url.username && !url.password
  } catch { return false }
}

export function WebToolResult({ name, text }: { name: 'web_search' | 'web_fetch'; text: string }) {
  const output = parseOutput(text)
  if (isRecord(output)) {
    if (name === 'web_search' && Array.isArray(output.results)) {
      return <ul className="my-2 space-y-2 break-words text-sm text-k-text">
        {output.results.length === 0 && <li>没有找到相关网页</li>}
        {output.results.map((item: unknown, index) => {
          if (!isRecord(item) || !webURL(item.url)) return null
          const excerpt = isString(item.snippet) && item.snippet ? item.snippet : item.summary
          return <li key={`${index}:${item.url}`}>
            <ExternalLink href={item.url}>{isString(item.title) && item.title ? item.title : item.url}</ExternalLink>
            {isString(excerpt) && excerpt && <p className="m-0 text-k-text-muted">{excerpt}</p>}
          </li>
        })}
      </ul>
    }
    if (name === 'web_fetch' && webURL(output.url) && isString(output.content)) {
      return <>
        <p className="my-2 break-words text-sm"><ExternalLink href={output.url}>{isString(output.title) && output.title ? output.title : output.url}</ExternalLink></p>
        <CodeBlock code={output.content} language="output" copyable={false} />
        {output.truncated === true && <p className="my-1 text-xs text-k-text-muted">仅显示正文开头，内容已截断</p>}
      </>
    }
  }
  return <CodeBlock code={text || '（空输出）'} language="output" copyable={false} />
}
