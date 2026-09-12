// 前端测试由 Bun 运行器执行（web/package.json 的 test 脚本为 bun test），
// 这里只声明测试用到的 Bun 专有模块，避免为了模块打桩再引入额外的类型依赖包。
declare module 'bun:test' {
  export const mock: {
    // 用桩实现替换模块，供测试断言间接依赖（例如 ECharts）。
    module(id: string, factory: () => unknown): void
    restore(): void
  }
}
