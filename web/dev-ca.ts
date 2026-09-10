import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

export function developmentCA(configuredPath = process.env.KAGUYA_CA_CERT, home = homedir()) {
  const path = configuredPath || join(home, '.kaguya', 'kaguya.crt')
  try {
    return readFileSync(path)
  } catch {
    throw new Error(`无法读取开发代理证书 ${path}。请先从系统配置下载当前证书，或在仓库根目录执行 ./target/kaguya --prepare-tls > ~/.kaguya/kaguya.crt；自定义位置请设置 KAGUYA_CA_CERT。`)
  }
}
