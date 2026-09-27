// 独立于人物头像的月相标识。继承 currentColor，可用于导航、欢迎态和回复。
export function LunarGlyph({ className = '' }: { className?: string }) {
  return <svg className={`lunar-glyph ${className}`} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
    <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="1.5" />
    <path d="M12 3a9 9 0 0 0 0 18Z" fill="currentColor" />
  </svg>
}
