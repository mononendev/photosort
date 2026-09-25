import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { execSync } from 'node:child_process'

function gitSha(): string {
  if (process.env.VITE_GIT_SHA) return process.env.VITE_GIT_SHA
  try {
    return execSync('git rev-parse HEAD', { stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim()
  } catch {
    return 'unknown'
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  define: {
    __APP_VERSION__: JSON.stringify(process.env.VITE_APP_VERSION || 'dev'),
    __GIT_SHA__: JSON.stringify(gitSha()),
    __BUILD_TIME__: JSON.stringify(process.env.VITE_BUILD_TIME || ''),
  },
  server: {
    host: '0.0.0.0',
    proxy: {
      '/api': { target: process.env.VITE_API_URL || 'http://localhost:8080', changeOrigin: true },
      '/media': { target: process.env.VITE_API_URL || 'http://localhost:8080', changeOrigin: true },
    },
  },
})
