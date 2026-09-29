import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// GM API 默认监听 127.0.0.1:8080；该端口被别的程序占用时可改跑其它端口，
// 用 VITE_GM_API_TARGET 指定代理目标，默认行为不变。
// 用 globalThis 读取，避免引入 @types/node 依赖（tsconfig.node.json 未装 node 类型）。
const gmApiTarget =
  (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env
    ?.VITE_GM_API_TARGET || 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': {
        target: gmApiTarget,
        changeOrigin: false,
      },
    },
  },
  build: { outDir: 'dist', sourcemap: false },
})
